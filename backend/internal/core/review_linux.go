//go:build linux

package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"time"
)

type ConflictReview struct {
	ID          string                    `json:"id"`
	Destination string                    `json:"destination"`
	Token       string                    `json:"token"`
	Files       map[string]TargetDecision `json:"files"`
	Required    int64                     `json:"requiredBytes"`
	Available   uint64                    `json:"availableBytes"`
}

// safeParent opens each directory beneath an anchored root, rejecting links.
// Rename operations use the returned directory descriptor, never absolute paths.
func safeParent(root *os.Root, name string) (*os.Root, string, error) {
	if _, err := ValidatePath(name); err != nil {
		return nil, "", err
	}
	parent := parentPath(name)
	if parent != "" {
		for _, p := range parents(parent) {
			st, err := root.Lstat(p)
			if err != nil {
				return nil, "", err
			}
			if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
				return nil, "", errors.New("destination contains a symbolic link or non-directory")
			}
		}
	} else {
		parent = "."
	}
	r, err := root.OpenRoot(parent)
	return r, filepath.Base(name), err
}
func existing(root *os.Root, e Entry) (TargetDecision, error) {
	p, base, err := safeParent(root, e.Path)
	if errors.Is(err, os.ErrNotExist) {
		return TargetDecision{}, nil
	}
	if err != nil {
		return TargetDecision{}, err
	}
	defer p.Close()
	st, err := p.Lstat(base)
	if errors.Is(err, os.ErrNotExist) {
		return TargetDecision{}, nil
	}
	if err != nil {
		return TargetDecision{}, err
	}
	if e.Dir {
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return TargetDecision{}, errors.New("directory conflicts with a file or symbolic link")
		}
		return TargetDecision{}, nil
	}
	if !st.Mode().IsRegular() {
		return TargetDecision{}, errors.New("destination conflicts with a non-regular file")
	}
	f, err := p.Open(base)
	if err != nil {
		return TargetDecision{}, err
	}
	defer f.Close()
	fs, err := f.Stat()
	if err != nil {
		return TargetDecision{}, err
	}
	if !os.SameFile(st, fs) {
		return TargetDecision{}, errors.New("destination changed while inspecting")
	}
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return TargetDecision{}, err
	}
	after, err := f.Stat()
	if err != nil {
		return TargetDecision{}, err
	}
	if fs.Size() != after.Size() || !fs.ModTime().Equal(after.ModTime()) {
		return TargetDecision{}, errors.New("destination changed while inspecting")
	}
	hash := hex.EncodeToString(h.Sum(nil))
	return TargetDecision{Hash: hash, Size: fs.Size(), Identical: hash == e.Hash && fs.Size() == e.Size}, nil
}
func (r *Receiver) review(id, dest string) (ConflictReview, error) {
	st := r.Store.Snapshot()
	t := st.Transfers[id]
	if t == nil || t.Direction != "incoming" || t.Status != "pending" {
		return ConflictReview{}, errors.New("request is not pending")
	}
	if dest == "" {
		dest = st.Settings.ReceiveDir
	}
	if !filepath.IsAbs(dest) {
		return ConflictReview{}, errors.New("destination must be absolute")
	}
	root, err := openDestination(dest)
	if err != nil {
		return ConflictReview{}, err
	}
	defer root.Close()
	v := ConflictReview{ID: id, Destination: filepath.Clean(dest), Files: map[string]TargetDecision{}}
	for _, e := range t.Manifest.Entries {
		d, err := existing(root, e)
		if err != nil {
			return v, fmt.Errorf("inspect %s: %w", e.Path, err)
		}
		if !e.Dir {
			v.Files[e.Path] = d
			if !d.Identical {
				v.Required += e.Size
			}
		}
	}
	v.Available, err = r.freeSpace(dest)
	if err != nil {
		return v, err
	}
	// Free space is advisory and intentionally excluded from the immutable review token.
	b, _ := json.Marshal([]any{t.Manifest.Digest, v.Destination, v.Files})
	h := sha256.Sum256(b)
	v.Token = hex.EncodeToString(h[:])
	return v, nil
}
func (r *Receiver) Review(id, dest string) (ConflictReview, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.review(id, dest)
}
func (r *Receiver) AcceptReviewed(id, dest, token string, overwrite bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, err := r.review(id, dest)
	if err != nil {
		return err
	}
	if token == "" || token != v.Token {
		return errors.New("destination or manifest changed; inspect conflicts again and use its review token")
	}
	for _, d := range v.Files {
		if d.Hash != "" && !d.Identical && !overwrite {
			return errors.New("different destination files exist; review them and explicitly approve --overwrite")
		}
	}
	if v.Available < uint64(v.Required)+(16<<20) {
		return errors.New("insufficient disk space for staged files and safety margin")
	}
	t := r.Store.Snapshot().Transfers[id]
	t.Destination = v.Destination
	stage, err := openStage(t)
	if err != nil {
		return err
	}
	defer stage.Close()
	done := make([]int64, len(t.Done))
	for i, e := range t.Manifest.Entries {
		if e.Dir {
			if err = ensureDirectory(stage, e.Path); err != nil {
				return err
			}
			continue
		}
		if v.Files[e.Path].Identical {
			done[i] = e.Size
			continue
		}
		if p := parentPath(e.Path); p != "" {
			if err = ensureDirectory(stage, p); err != nil {
				return err
			}
		}
		f, err := stage.OpenFile(e.Path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if errors.Is(err, os.ErrExist) {
			info, se := stage.Lstat(e.Path)
			if se != nil || !info.Mode().IsRegular() {
				return errors.New("unsafe staged file")
			}
			continue
		}
		if err != nil {
			return err
		}
		err = f.Sync()
		f.Close()
		if err != nil {
			return err
		}
	}
	return r.Store.Update(func(s *State) error {
		x := s.Transfers[id]
		if x == nil || x.Status != "pending" || s.Peers[x.PeerID] == nil {
			return errors.New("request changed")
		}
		x.Reviewed = true
		x.Targets = v.Files
		x.Approved = true
		x.Destination = v.Destination
		x.Done = done
		x.Status = "queued"
		x.Updated = time.Now().UTC()
		return nil
	})
}
func moveReviewed(from, to *os.Root, src, dst string) error {
	a, an, err := safeParent(from, src)
	if err != nil {
		return err
	}
	defer a.Close()
	b, bn, err := safeParent(to, dst)
	if err != nil {
		return err
	}
	defer b.Close()
	af, err := a.Open(".")
	if err != nil {
		return err
	}
	defer af.Close()
	bf, err := b.Open(".")
	if err != nil {
		return err
	}
	defer bf.Close()
	if err = unix.Renameat2(int(af.Fd()), an, int(bf.Fd()), bn, unix.RENAME_NOREPLACE); err != nil {
		return err
	}
	if err = af.Sync(); err != nil {
		return err
	}
	return bf.Sync()
}
func backupName(id string) string { return ".handoff-partials/" + id + "-replaced" }
func (r *Receiver) finishReviewed(t *Transfer) error {
	root, err := openDestination(t.Destination)
	if err != nil {
		return err
	}
	defer root.Close()
	stage, err := openStage(t)
	if err != nil {
		return err
	}
	defer stage.Close()
	for i, e := range t.Manifest.Entries {
		if e.Dir {
			if err = ensureDirectory(root, e.Path); err != nil {
				return err
			}
			continue
		}
		if t.Done[i] != e.Size {
			return errors.New("transfer incomplete")
		}
		d, ok := t.Targets[e.Path]
		if !ok {
			return errors.New("missing destination approval")
		}
		if d.Identical {
			if err = verifyFile(root, e.Path, e); err != nil {
				return fmt.Errorf("previously identical file changed: %w", err)
			}
			continue
		}
		if _, err = stage.Lstat(e.Path); errors.Is(err, os.ErrNotExist) {
			if err = verifyFile(root, e.Path, e); err != nil {
				return err
			}
			continue
		} else if err != nil {
			return err
		}
		if err = verifyFile(stage, e.Path, e); err != nil {
			return err
		}
		if d.Hash != "" {
			if err = ensureDirectory(root, backupName(t.ID)); err != nil {
				return err
			}
			backup, err := root.OpenRoot(backupName(t.ID))
			if err != nil {
				return err
			}
			if p := parentPath(e.Path); p != "" {
				err = ensureDirectory(backup, p)
			}
			old := Entry{Path: e.Path, Size: d.Size, Hash: d.Hash}
			if err == nil {
				if _, se := backup.Lstat(e.Path); errors.Is(se, os.ErrNotExist) {
					if err = verifyFile(root, e.Path, old); err == nil {
						err = moveReviewed(root, backup, e.Path, e.Path)
					}
				} else {
					err = se
				}
			}
			// Also verify after moving: concurrent local replacement cannot be destroyed.
			if err == nil {
				err = verifyFile(backup, e.Path, old)
			}
			backup.Close()
			if err != nil {
				return fmt.Errorf("replacement stopped; original preserved under %s: %w", backupName(t.ID), err)
			}
		}
		if err = moveReviewed(stage, root, e.Path, e.Path); err != nil {
			return fmt.Errorf("destination changed; staged and replaced files retained: %w", err)
		}
	}
	// Persist empty/deep directory entries too, before marking the whole request
	// completed. File moves already sync their source/destination parents.
	for i := len(t.Manifest.Entries) - 1; i >= 0; i-- {
		e := t.Manifest.Entries[i]
		if e.Dir {
			f, se := root.Open(e.Path)
			if se != nil {
				return se
			}
			se = f.Sync()
			f.Close()
			if se != nil {
				return se
			}
		}
	}
	rf, se := root.Open(".")
	if se != nil {
		return se
	}
	se = rf.Sync()
	rf.Close()
	if se != nil {
		return se
	}
	if err = r.Store.Update(func(s *State) error {
		x := s.Transfers[t.ID]
		x.Status = "completed"
		x.Error = ""
		x.Updated = time.Now().UTC()
		return nil
	}); err != nil {
		return err
	}
	// Backups are retained deliberately for recovery from local concurrent edits.
	// Removing history does not delete these files; the CLI documents their location.
	return root.RemoveAll(stageName(t.ID))
}
func (r *Receiver) recoverReviewed(t *Transfer) error {
	stage, err := openStage(t)
	if err != nil {
		return err
	}
	defer stage.Close()
	root, err := openDestination(t.Destination)
	if err != nil {
		return err
	}
	defer root.Close()
	done := make([]int64, len(t.Done))
	buf := make([]byte, ChunkSize)
	for i, e := range t.Manifest.Entries {
		if e.Dir {
			if err = ensureDirectory(stage, e.Path); err != nil {
				return err
			}
			continue
		}
		d, ok := t.Targets[e.Path]
		if !ok {
			return errors.New("missing destination review")
		}
		_, se := stage.Lstat(e.Path)
		if d.Identical {
			if err = verifyFile(root, e.Path, e); err != nil {
				return fmt.Errorf("previously identical file changed: %w", err)
			}
			done[i] = e.Size
			continue
		}
		if errors.Is(se, os.ErrNotExist) {
			if err = verifyFile(root, e.Path, e); err == nil {
				done[i] = e.Size
				continue
			}
			if _, be := root.Lstat(backupName(t.ID) + "/" + e.Path); be == nil {
				return errors.New("finalized replacement changed; staged file missing and original retained in backup")
			} else if !errors.Is(be, os.ErrNotExist) {
				return be
			}
			// A missing partial is not a verified checkpoint. Recreate it and resume
			// from zero; exclusive finalization still protects any concurrent edit.
			if p := parentPath(e.Path); p != "" {
				if ce := ensureDirectory(stage, p); ce != nil {
					return ce
				}
			}
			f, ce := stage.OpenFile(e.Path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
			if ce != nil {
				return ce
			}
			ce = f.Sync()
			f.Close()
			if ce != nil {
				return ce
			}
			se = nil
		}
		if se != nil {
			return se
		}
		st, err := stage.Lstat(e.Path)
		if err != nil {
			return err
		}
		if !st.Mode().IsRegular() {
			return errors.New("unsafe staged file")
		}
		f, err := stage.OpenFile(e.Path, os.O_RDWR, 0600)
		if err != nil {
			return err
		}
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
			return err
		}
	}
	return r.Store.Update(func(s *State) error {
		x := s.Transfers[t.ID]
		x.Done = done
		if !x.Paused {
			x.Status = "queued"
		}
		return nil
	})
}
