package core

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type Receiver struct {
	Store *Store
	mu    sync.Mutex
}

func (r *Receiver) Offer(peer string, m Manifest) error {
	if err := ValidateManifest(m); err != nil {
		return err
	}
	return r.Store.Update(func(s *State) error {
		if s.Peers[peer] == nil {
			return errors.New("peer revoked")
		}
		if receipt, ok := s.Receipts[m.ID]; ok {
			if receipt.PeerID != peer || receipt.Digest != m.Digest {
				return errors.New("transfer identity collision")
			}
			return nil
		}
		if old := s.Transfers[m.ID]; old != nil {
			if old.PeerID != peer || old.Direction != "incoming" || old.Manifest.Digest != m.Digest {
				return errors.New("transfer identity collision")
			}
			return nil
		}
		count := 0
		for _, t := range s.Transfers {
			if t.PeerID == peer && !Terminal(t.Status) {
				count++
			}
		}
		if count >= 256 {
			return errors.New("too many outstanding requests")
		}
		now := time.Now().UTC()
		s.Transfers[m.ID] = &Transfer{ID: m.ID, PeerID: peer, Direction: "incoming", Manifest: m, Status: "pending", Done: make([]int64, len(m.Entries)), Created: now, Updated: now, Order: now.UnixNano()}
		return nil
	})
}
func stageName(id string) string { return ".handoff-partials/" + id }
func openDestination(dir string) (*os.Root, error) {
	if !filepath.IsAbs(dir) {
		return nil, errors.New("destination must be absolute")
	}
	return os.OpenRoot(dir)
}
func ensureDirectory(root *os.Root, name string) error {
	for _, p := range parents(name) {
		st, e := root.Lstat(p)
		if errors.Is(e, os.ErrNotExist) {
			if e = root.Mkdir(p, 0700); e != nil {
				return e
			}
			continue
		}
		if e != nil {
			return e
		}
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return errors.New("unsafe staging directory")
		}
	}
	return nil
}
func parents(p string) []string {
	parts := strings.Split(p, "/")
	out := make([]string, len(parts))
	for i := range parts {
		out[i] = strings.Join(parts[:i+1], "/")
	}
	return out
}
func openStage(t *Transfer) (*os.Root, error) {
	root, e := openDestination(t.Destination)
	if e != nil {
		return nil, e
	}
	defer root.Close()
	if e = ensureDirectory(root, stageName(t.ID)); e != nil {
		return nil, e
	}
	return root.OpenRoot(stageName(t.ID))
}
func (r *Receiver) Accept(id, dest string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.Store.Snapshot()
	t := s.Transfers[id]
	if t == nil || t.Direction != "incoming" || t.Status != "pending" {
		return errors.New("request is not pending")
	}
	if dest == "" {
		dest = s.Settings.ReceiveDir
	}
	abs, e := filepath.Abs(dest)
	if e != nil {
		return e
	}
	dest = abs
	root, e := openDestination(dest)
	if e != nil {
		return e
	}
	root.Close()
	free, e := available(dest)
	if e != nil {
		return e
	}
	if free < uint64(t.Manifest.Total)+(16<<20) {
		return errors.New("insufficient disk space")
	}
	t.Destination = dest
	stage, e := openStage(t)
	if e != nil {
		return e
	}
	defer stage.Close()
	for _, ent := range t.Manifest.Entries {
		if ent.Dir {
			if e = ensureDirectory(stage, ent.Path); e != nil {
				return e
			}
		} else {
			if p := parentPath(ent.Path); p != "" {
				if e = ensureDirectory(stage, p); e != nil {
					return e
				}
			}
			f, e := stage.OpenFile(ent.Path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if errors.Is(e, os.ErrExist) {
				st, se := stage.Lstat(ent.Path)
				if se != nil || !st.Mode().IsRegular() {
					return errors.New("unsafe staged file")
				}
				continue
			}
			if e != nil {
				return e
			}
			if e = f.Sync(); e != nil {
				f.Close()
				return e
			}
			f.Close()
		}
	}
	return r.Store.Update(func(s *State) error {
		t := s.Transfers[id]
		if t == nil || t.Status != "pending" {
			return errors.New("request changed")
		}
		t.Approved = true
		t.Destination = dest
		t.Status = "queued"
		t.Updated = time.Now().UTC()
		return nil
	})
}
func (r *Receiver) Put(peer, id, digest string, index int, offset int64, data []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.Store.Snapshot()
	t := s.Transfers[id]
	if s.Peers[peer] == nil || t == nil || t.PeerID != peer || t.Direction != "incoming" || t.Manifest.Digest != digest || !t.Approved || t.Paused || Terminal(t.Status) {
		return errors.New("transfer is not approved and available")
	}
	if index < 0 || index >= len(t.Manifest.Entries) {
		return errors.New("invalid file index")
	}
	e := t.Manifest.Entries[index]
	if e.Dir || offset < 0 || offset%ChunkSize != 0 || offset >= e.Size || len(data) != int(min(int64(ChunkSize), e.Size-offset)) {
		return errors.New("invalid chunk")
	}
	for i, prior := range t.Manifest.Entries {
		if i >= index {
			break
		}
		if !prior.Dir && t.Done[i] != prior.Size {
			return errors.New("files must transfer sequentially")
		}
	}
	for _, other := range s.Transfers {
		if other.ID != id && other.Direction == "incoming" && other.Status == "active" && !other.Paused && time.Since(other.Updated) < 10*time.Minute {
			return errors.New("receiver busy; queued")
		}
	}
	h := sha256.Sum256(data)
	if hex.EncodeToString(h[:]) != e.Chunks[offset/ChunkSize] {
		return errors.New("chunk integrity failure")
	}
	if offset < t.Done[index] {
		return nil
	}
	if offset != t.Done[index] {
		return errors.New("nonsequential chunk")
	}
	free, err := available(t.Destination)
	if err != nil {
		return err
	}
	if free < uint64(len(data)) {
		return errors.New("insufficient disk space")
	}
	stage, err := openStage(t)
	if err != nil {
		return err
	}
	defer stage.Close()
	f, err := stage.OpenFile(e.Path, os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = f.WriteAt(data, offset); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	return r.Store.Update(func(s *State) error {
		t := s.Transfers[id]
		if s.Peers[peer] == nil || t == nil || Terminal(t.Status) || t.Paused {
			return errors.New("transfer canceled or paused")
		}
		t.Done[index] = offset + int64(len(data))
		t.Status = "active"
		if t.Done[index] == e.Size {
			t.Status = "queued"
		}
		t.Error = ""
		t.Updated = time.Now().UTC()
		return nil
	})
}
func verifyFile(root *os.Root, p string, e Entry) error {
	st, err := root.Lstat(p)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() || st.Size() != e.Size {
		return errors.New("received file size or type mismatch")
	}
	f, err := root.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != e.Hash {
		return errors.New("received file integrity failure")
	}
	return nil
}
func roots(m Manifest) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range m.Entries {
		p := strings.SplitN(e.Path, "/", 2)[0]
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}
func ConflictName(name string, n int, dir bool) string {
	if n == 0 {
		return name
	}
	ext := ""
	if !dir {
		ext = path.Ext(name)
		if ext == name {
			ext = ""
		}
	}
	suffix := fmt.Sprintf(" (%d)", n)
	stem := strings.TrimSuffix(name, ext)
	if len(ext) > 80 {
		ext = ext[:80]
		for !utf8.ValidString(ext) {
			ext = ext[:len(ext)-1]
		}
	}
	budget := 240 - len(suffix) - len(ext)
	if len(stem) > budget {
		stem = stem[:budget]
		for !utf8.ValidString(stem) {
			stem = stem[:len(stem)-1]
		}
	}
	return stem + suffix + ext
}
func (r *Receiver) Finish(peer, id, digest string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.Store.Snapshot()
	t := s.Transfers[id]
	if s.Peers[peer] == nil || t == nil || t.PeerID != peer || !t.Approved || t.Manifest.Digest != digest || t.Paused {
		return errors.New("not approved")
	}
	if t.Status == "completed" {
		return nil
	}
	if Terminal(t.Status) {
		return errors.New("transfer ended")
	}
	root, err := openDestination(t.Destination)
	if err != nil {
		return err
	}
	defer root.Close()
	for i, e := range t.Manifest.Entries {
		if !e.Dir && t.Done[i] != e.Size {
			return errors.New("transfer incomplete")
		}
	}
	stage, err := openStage(t)
	if err != nil {
		return err
	}
	defer stage.Close()
	for _, top := range roots(t.Manifest) {
		old := t.Final[top]
		_, stageErr := stage.Lstat(top)
		if errors.Is(stageErr, os.ErrNotExist) && old != "" { // Rename committed just before a crash: verify before trusting it.
			if err = verifyTree(root, old, top, t.Manifest); err != nil {
				return err
			}
			continue
		}
		if stageErr != nil {
			return stageErr
		}
		if err = verifyTree(stage, top, top, t.Manifest); err != nil {
			return err
		}
		dir := false
		for _, e := range t.Manifest.Entries {
			if e.Path == top {
				dir = e.Dir
				break
			}
		}
		rf, err := root.Open(".")
		if err != nil {
			return err
		}
		moved := false
		for n := 0; n < 100000; n++ {
			candidate := ConflictName(top, n, dir)
			if _, err = ValidatePath(candidate); err != nil {
				rf.Close()
				return err
			}
			if _, err = root.Lstat(candidate); err == nil {
				continue
			} else if !errors.Is(err, os.ErrNotExist) {
				rf.Close()
				return err
			}
			err = r.Store.Update(func(s *State) error {
				x := s.Transfers[id]
				if x.Final == nil {
					x.Final = map[string]string{}
				}
				x.Final[top] = candidate
				return nil
			})
			if err != nil {
				rf.Close()
				return err
			}
			err = renameExclusive(rf, stageName(id)+"/"+top, candidate)
			if errors.Is(err, os.ErrExist) {
				continue
			}
			if err != nil {
				rf.Close()
				return err
			}
			t.Final = r.Store.Snapshot().Transfers[id].Final
			moved = true
			break
		}
		rf.Close()
		if !moved {
			return errors.New("could not allocate conflict-free filename")
		}
	}
	if err = syncDir(t.Destination); err != nil {
		return err
	}
	err = r.Store.Update(func(s *State) error {
		t := s.Transfers[id]
		t.Status = "completed"
		t.Error = ""
		t.Updated = time.Now().UTC()
		return nil
	})
	if err == nil {
		root.RemoveAll(stageName(id))
	}
	return err
}
func verifyTree(root *os.Root, target, top string, m Manifest) error {
	for _, e := range m.Entries {
		if e.Path != top && !strings.HasPrefix(e.Path, top+"/") {
			continue
		}
		p := target + strings.TrimPrefix(e.Path, top)
		if e.Dir {
			st, err := root.Lstat(p)
			if err != nil {
				return err
			}
			if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
				return errors.New("unsafe received directory")
			}
		} else if err := verifyFile(root, p, e); err != nil {
			return err
		}
	}
	return nil
}

// Recover never trusts a saved offset alone. Verify every on-disk acknowledged
// chunk and truncate an unverified tail before exposing progress to the sender.
func (r *Receiver) Recover() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result error
	for _, t := range r.Store.Snapshot().Transfers {
		if t.Direction != "incoming" || !t.Approved || Terminal(t.Status) {
			continue
		}
		if err := r.recoverOne(t); err != nil {
			result = errors.Join(result, err)
			if e := r.Store.Update(func(s *State) error {
				x := s.Transfers[t.ID]
				x.Status = "failed"
				x.Paused = true
				x.Error = Redact(err.Error())
				return nil
			}); e != nil {
				result = errors.Join(result, e)
			}
		}
	}
	return result
}
func (r *Receiver) recoverOne(t *Transfer) error {
	id := t.ID
	stage, err := openStage(t)
	if err != nil {
		return err
	}
	done := make([]int64, len(t.Done))
	for i, e := range t.Manifest.Entries {
		if e.Dir {
			top := strings.SplitN(e.Path, "/", 2)[0]
			if t.Final[top] != "" {
				if _, err := stage.Lstat(top); errors.Is(err, os.ErrNotExist) {
					continue
				}
			}
			if err := ensureDirectory(stage, e.Path); err != nil {
				stage.Close()
				return err
			}
			continue
		}
		top := strings.SplitN(e.Path, "/", 2)[0]
		if dest := t.Final[top]; dest != "" {
			if _, err := stage.Lstat(top); errors.Is(err, os.ErrNotExist) {
				root, err := openDestination(t.Destination)
				if err != nil {
					stage.Close()
					return err
				}
				err = verifyTree(root, dest, top, t.Manifest)
				root.Close()
				if err != nil {
					stage.Close()
					return err
				}
				done[i] = e.Size
				continue
			}
		}
		f, err := stage.OpenFile(e.Path, os.O_RDWR|os.O_CREATE, 0600)
		if err != nil {
			stage.Close()
			return err
		}
		buf := make([]byte, ChunkSize)
		for done[i] < t.Done[i] {
			n := min(int64(ChunkSize), e.Size-done[i])
			if _, err = f.ReadAt(buf[:n], done[i]); err != nil {
				break
			}
			h := sha256.Sum256(buf[:n])
			if hex.EncodeToString(h[:]) != e.Chunks[done[i]/ChunkSize] {
				break
			}
			done[i] += n
		}
		err = f.Truncate(done[i])
		if err == nil {
			err = f.Sync()
		}
		f.Close()
		if err != nil {
			stage.Close()
			return err
		}
	}
	stage.Close()
	if err = r.Store.Update(func(s *State) error {
		x := s.Transfers[id]
		x.Done = done
		if !x.Paused {
			x.Status = "queued"
		}
		return nil
	}); err != nil {
		return err
	}
	return nil
}

func (r *Receiver) Change(id, action string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if action == "retry" || action == "resume" {
		t := r.Store.Snapshot().Transfers[id]
		if t != nil && t.Direction == "incoming" && t.Approved && !Terminal(t.Status) {
			if err := r.recoverOne(t); err != nil {
				return err
			}
		}
	}
	return r.Store.Update(func(s *State) error {
		t := s.Transfers[id]
		if t == nil {
			return errors.New("unknown transfer")
		}
		if Terminal(t.Status) {
			return errors.New("transfer already ended")
		}
		switch action {
		case "decline":
			if t.Status != "pending" || t.Direction != "incoming" {
				return errors.New("not an incoming request")
			}
			t.Status = "declined"
		case "cancel":
			t.Status = "canceled"
		case "pause":
			if !t.Approved && t.Direction == "incoming" {
				return errors.New("not approved")
			}
			t.Paused = true
			t.Status = "paused"
		case "resume":
			t.Paused = false
			if t.Approved {
				t.Status = "queued"
			} else {
				t.Status = "pending"
			}
			t.Error = ""
		case "retry":
			t.Error = ""
			t.Paused = false
			if t.Approved {
				t.Status = "queued"
			} else {
				t.Status = "pending"
			}
		default:
			return errors.New("unknown action")
		}
		t.Updated = time.Now().UTC()
		return nil
	})
}
func (r *Receiver) CleanupCanceled() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range r.Store.Snapshot().Transfers {
		if t.Direction == "incoming" && (t.Status == "canceled" || t.Status == "declined") && t.Destination != "" {
			root, e := openDestination(t.Destination)
			if e != nil {
				return e
			}
			e = root.RemoveAll(stageName(t.ID))
			root.Close()
			if e != nil {
				return e
			}
		}
	}
	return nil
}

func (r *Receiver) RevokePeer(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Store.Update(func(st *State) error {
		if st.Peers[id] == nil {
			return errors.New("unknown peer")
		}
		delete(st.Peers, id)
		for _, t := range st.Transfers {
			if t.PeerID == id && !Terminal(t.Status) {
				t.Status = "canceled"
				t.Error = "Peer removed"
			}
		}
		return nil
	})
}

// Grant reserves the receiver's next file before a sender transmits its body.
// This avoids competing downloads when several paired senders are active.
func (r *Receiver) Grant(peer, id, digest string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.Store.Snapshot()
	t := st.Transfers[id]
	if st.Peers[peer] == nil || t == nil || t.PeerID != peer || t.Direction != "incoming" || t.Manifest.Digest != digest {
		return false, errors.New("unknown authenticated transfer")
	}
	if !t.Approved || t.Paused || Terminal(t.Status) || t.Status == "failed" {
		return false, nil
	}
	for _, other := range st.Transfers {
		if other.ID != id && other.Direction == "incoming" && other.Status == "active" && !other.Paused && time.Since(other.Updated) < 10*time.Minute {
			return false, nil
		}
	}
	err := r.Store.Update(func(st *State) error {
		x := st.Transfers[id]
		x.Status = "active"
		x.Updated = time.Now().UTC()
		return nil
	})
	return err == nil, err
}
