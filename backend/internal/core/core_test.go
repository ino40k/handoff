package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"tailscale.com/types/key"
	"testing"
	"time"
)

func setup(t *testing.T) (*Store, *Receiver, string, string) {
	t.Helper()
	base := t.TempDir()
	dest := filepath.Join(base, "receive")
	os.Mkdir(dest, 0700)
	s, e := NewStore(filepath.Join(base, "data"), dest)
	if e != nil {
		t.Fatal(e)
	}
	remote := key.NewNode()
	id := PeerID(remote.Public())
	e = s.Update(func(st *State) error {
		st.Peers[id] = &Peer{ID: id, Name: "test peer", ServerKey: remote.Public(), RemoteKey: key.NewNode().Public(), LocalKey: key.NewNode()}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	return s, &Receiver{Store: s}, dest, id
}
func manifest(t *testing.T, data []byte) (Manifest, string) {
	t.Helper()
	src := filepath.Join(t.TempDir(), "video.bin")
	if e := os.WriteFile(src, data, 0600); e != nil {
		t.Fatal(e)
	}
	m, e := BuildManifest(context.Background(), []string{src})
	if e != nil {
		t.Fatal(e)
	}
	return m, src
}
func putAll(t *testing.T, r *Receiver, peer string, m Manifest) {
	t.Helper()
	for i, e := range m.Entries {
		for offset := int64(0); offset < e.Size; offset += ChunkSize {
			b, err := ReadChunk(e, offset)
			if err != nil {
				t.Fatal(err)
			}
			if err = r.Put(peer, m.ID, m.Digest, i, offset, b); err != nil {
				t.Fatal(err)
			}
		}
	}
}
func TestApprovalIntegrityRecoveryAndConflict(t *testing.T) {
	s, r, dest, peer := setup(t)
	data := bytes.Repeat([]byte("verified!"), ChunkSize/9+50)
	m, _ := manifest(t, data)
	if e := r.Offer(peer, m.Public()); e != nil {
		t.Fatal(e)
	}
	if e := r.Put(peer, m.ID, m.Digest, 0, 0, data[:ChunkSize]); e == nil {
		t.Fatal("unapproved write accepted")
	}
	before := s.Snapshot()
	s2, e := NewStore(filepath.Dir(s.path), dest)
	if e != nil {
		t.Fatal(e)
	}
	if !s2.Snapshot().Identity.Key.Equal(before.Identity.Key) || s2.Snapshot().Transfers[m.ID].Status != "pending" {
		t.Fatal("identity or pending request not durable")
	}
	r = &Receiver{Store: s2}
	if e = r.Accept(m.ID, ""); e != nil {
		t.Fatal(e)
	}
	bad := bytes.Clone(data[:ChunkSize])
	bad[0] ^= 1
	if e = r.Put(peer, m.ID, m.Digest, 0, 0, bad); e == nil {
		t.Fatal("bad hash accepted")
	}
	if e = r.Put(peer, m.ID, m.Digest, 0, 0, data[:ChunkSize]); e != nil {
		t.Fatal(e)
	}
	// Simulate bytes written after the last committed checkpoint.
	stage := filepath.Join(dest, stageName(m.ID), "video.bin")
	f, _ := os.OpenFile(stage, os.O_WRONLY|os.O_APPEND, 0600)
	f.Write([]byte("unverified tail"))
	f.Close()
	s3, e := NewStore(filepath.Dir(s.path), dest)
	if e != nil {
		t.Fatal(e)
	}
	r = &Receiver{Store: s3}
	if e = r.Recover(); e != nil {
		t.Fatal(e)
	}
	st, _ := os.Stat(stage)
	if st.Size() != ChunkSize || !s3.Snapshot().Transfers[m.ID].Approved {
		t.Fatal("resume checkpoint or approval lost")
	}
	os.WriteFile(filepath.Join(dest, "video.bin"), []byte("existing"), 0600)
	putAll(t, r, peer, m)
	if e = r.Finish(peer, m.ID, m.Digest); e != nil {
		t.Fatal(e)
	}
	got, e := os.ReadFile(filepath.Join(dest, "video (1).bin"))
	if e != nil || !bytes.Equal(got, data) {
		t.Fatal("content or conflict name incorrect", e)
	}
	old, _ := os.ReadFile(filepath.Join(dest, "video.bin"))
	if string(old) != "existing" {
		t.Fatal("overwrote existing file")
	}
	if r.Store.Snapshot().Transfers[m.ID].Status != "completed" {
		t.Fatal("not completed")
	}
}
func TestRecoveryRechecksAcknowledgedChunks(t *testing.T) {
	s, r, dest, peer := setup(t)
	m, _ := manifest(t, bytes.Repeat([]byte{7}, ChunkSize+32))
	r.Offer(peer, m.Public())
	r.Accept(m.ID, "")
	b, _ := ReadChunk(m.Entries[0], 0)
	r.Put(peer, m.ID, m.Digest, 0, 0, b)
	f, _ := os.OpenFile(filepath.Join(dest, stageName(m.ID), "video.bin"), os.O_WRONLY, 0600)
	f.WriteAt([]byte{8}, 12)
	f.Close()
	if e := r.Recover(); e != nil {
		t.Fatal(e)
	}
	if s.Snapshot().Transfers[m.ID].Done[0] != 0 {
		t.Fatal("trusted corrupt checkpoint")
	}
}
func TestManifestSubstitutionAndSenderChange(t *testing.T) {
	_, r, _, peer := setup(t)
	m, src := manifest(t, []byte("original"))
	r.Offer(peer, m.Public())
	changed := m.Public()
	changed.Entries[0].Hash = hex.EncodeToString(make([]byte, 32))
	changed.Digest = changed.CalculateDigest()
	if e := r.Offer(peer, changed); e == nil {
		t.Fatal("manifest substitution allowed")
	}
	os.WriteFile(src, []byte("replaced"), 0600)
	if _, e := ReadChunk(m.Entries[0], 0); e == nil {
		t.Fatal("source substitution allowed")
	}
}
func TestPathsAndSymlinks(t *testing.T) {
	for _, p := range []string{"../escape", "/absolute", "a/../b", "C:/bad", "a\\b", "a//b", "CON.txt", "COM1", "a.", ".handoff-partials/a", "a\x00b"} {
		if _, e := ValidatePath(p); e == nil {
			t.Fatalf("allowed %q", p)
		}
	}
	_, r, dest, peer := setup(t)
	m, _ := manifest(t, []byte("data"))
	if err := os.Symlink(t.TempDir(), filepath.Join(dest, ".handoff-partials")); err != nil {
		t.Skip("OS does not permit creating test symlinks: " + err.Error())
	}
	r.Offer(peer, m.Public())
	if e := r.Accept(m.ID, ""); e == nil {
		t.Fatal("symlink staging accepted")
	}
	sourceLink := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(t.TempDir(), sourceLink); err != nil {
		t.Fatal(err)
	}
	if _, e := BuildManifest(context.Background(), []string{sourceLink}); e == nil {
		t.Fatal("symlink source accepted")
	}
}
func TestDirectoriesEmptyFilesAndFinalizationCrash(t *testing.T) {
	s, r, dest, peer := setup(t)
	base := t.TempDir()
	folder := filepath.Join(base, "photos")
	os.MkdirAll(filepath.Join(folder, "empty"), 0700)
	os.WriteFile(filepath.Join(folder, "zero"), nil, 0600)
	os.WriteFile(filepath.Join(folder, "one.txt"), []byte("one"), 0600)
	m, e := BuildManifest(context.Background(), []string{folder})
	if e != nil {
		t.Fatal(e)
	}
	r.Offer(peer, m.Public())
	if e = r.Accept(m.ID, ""); e != nil {
		t.Fatal(e)
	}
	putAll(t, r, peer, m)
	s.Update(func(st *State) error { st.Transfers[m.ID].Final = map[string]string{"photos": "photos"}; return nil })
	os.Rename(filepath.Join(dest, stageName(m.ID), "photos"), filepath.Join(dest, "photos"))
	if e = r.Recover(); e != nil {
		t.Fatal(e)
	}
	if e = r.Finish(peer, m.ID, m.Digest); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(dest, "photos", "empty")); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(dest, "photos", "zero")); e != nil {
		t.Fatal(e)
	}
}
func TestPauseCancelPeerOwnershipAndSequentialWrites(t *testing.T) {
	s, r, _, peer := setup(t)
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "a"), []byte("a"), 0600)
	os.WriteFile(filepath.Join(src, "b"), []byte("b"), 0600)
	m, e := BuildManifest(context.Background(), []string{src})
	if e != nil {
		t.Fatal(e)
	}
	r.Offer(peer, m.Public())
	r.Accept(m.ID, "")
	indices := []int{}
	for i, e := range m.Entries {
		if !e.Dir {
			indices = append(indices, i)
		}
	}
	b, _ := ReadChunk(m.Entries[indices[1]], 0)
	if e = r.Put(peer, m.ID, m.Digest, indices[1], 0, b); e == nil {
		t.Fatal("nonsequential file accepted")
	}
	r.Change(m.ID, "pause")
	b, _ = ReadChunk(m.Entries[indices[0]], 0)
	if e = r.Put(peer, m.ID, m.Digest, indices[0], 0, b); e == nil {
		t.Fatal("paused write")
	}
	r.Change(m.ID, "resume")
	if e = r.Put("another peer", m.ID, m.Digest, indices[0], 0, b); e == nil {
		t.Fatal("wrong peer")
	}
	r.Change(m.ID, "cancel")
	if e = r.Put(peer, m.ID, m.Digest, indices[0], 0, b); e == nil {
		t.Fatal("canceled write")
	}
	s.Update(func(st *State) error { delete(st.Peers, peer); return nil })
	if e = r.Offer(peer, m.Public()); e == nil {
		t.Fatal("revoked peer accepted")
	}
}
func TestStoreTransactionAndCorruption(t *testing.T) {
	s, _, dest, _ := setup(t)
	old := s.Snapshot().Settings.Name
	expected := errors.New("abort")
	if e := s.Update(func(st *State) error { st.Settings.Name = "changed"; return expected }); e != expected {
		t.Fatal(e)
	}
	if s.Snapshot().Settings.Name != old {
		t.Fatal("aborted transaction visible")
	}
	os.WriteFile(s.path, []byte("corrupt"), 0600)
	if _, e := NewStore(filepath.Dir(s.path), dest); e == nil {
		t.Fatal("silently reset corrupt identity")
	}
}
func TestManifestDuplicateAndHierarchy(t *testing.T) {
	m, _ := manifest(t, []byte("x"))
	m = m.Public()
	m.Entries = append(m.Entries, m.Entries[0])
	m.Total *= 2
	m.Digest = m.CalculateDigest()
	if e := ValidateManifest(m); e == nil {
		t.Fatal("duplicate path accepted")
	}
	m.Entries = m.Entries[:1]
	m.Total /= 2
	m.Entries[0].Path = "missing/a"
	m.Digest = m.CalculateDigest()
	if e := ValidateManifest(m); e == nil {
		t.Fatal("missing parent accepted")
	}
}
func TestLoggingRedactsSecrets(t *testing.T) {
	key := "privkey:" + hex.EncodeToString(make([]byte, 32))
	text := Redact("tc" + hex.EncodeToString(make([]byte, 32)) + " " + key + " handoff1." + hex.EncodeToString(make([]byte, 64)))
	if bytes.Contains([]byte(text), []byte(key)) {
		t.Fatal("secret leaked")
	}
	h := sha256.Sum256([]byte("x"))
	_ = h
	_ = time.Second
}
func TestReceiverGrantSerializesSenders(t *testing.T) {
	_, r, _, peer := setup(t)
	one, _ := manifest(t, []byte("one"))
	two, _ := manifest(t, []byte("two"))
	r.Offer(peer, one.Public())
	r.Offer(peer, two.Public())
	r.Accept(one.ID, "")
	r.Accept(two.ID, "")
	ok, e := r.Grant(peer, one.ID, one.Digest)
	if e != nil || !ok {
		t.Fatal("first grant", e)
	}
	ok, e = r.Grant(peer, two.ID, two.Digest)
	if e != nil || ok {
		t.Fatal("parallel sender granted")
	}
	putAll(t, r, peer, one)
	ok, e = r.Grant(peer, two.ID, two.Digest)
	if e != nil || !ok {
		t.Fatal("queue did not advance", e)
	}
}
func TestDirectoryConflictNames(t *testing.T) {
	_, r, dest, peer := setup(t)
	folder := filepath.Join(t.TempDir(), "archive.backup")
	os.Mkdir(folder, 0700)
	os.WriteFile(filepath.Join(folder, "new.txt"), []byte("new"), 0600)
	os.Mkdir(filepath.Join(dest, "archive.backup"), 0700)
	os.WriteFile(filepath.Join(dest, "archive.backup", "old.txt"), []byte("old"), 0600)
	m, e := BuildManifest(context.Background(), []string{folder})
	if e != nil {
		t.Fatal(e)
	}
	r.Offer(peer, m.Public())
	r.Accept(m.ID, "")
	putAll(t, r, peer, m)
	if e = r.Finish(peer, m.ID, m.Digest); e != nil {
		t.Fatal(e)
	}
	newFile, e := os.ReadFile(filepath.Join(dest, "archive.backup (1)", "new.txt"))
	if e != nil || string(newFile) != "new" {
		t.Fatal("directory conflict failed", e)
	}
	old, e := os.ReadFile(filepath.Join(dest, "archive.backup", "old.txt"))
	if e != nil || string(old) != "old" {
		t.Fatal("existing directory altered", e)
	}
}
