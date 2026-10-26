//go:build linux

package core

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func must(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func TestReviewedMergeSkipOverwriteAndResume(t *testing.T) {
	s, r, dest, peer := setup(t)
	src := filepath.Join(t.TempDir(), "collection")
	must(t, os.MkdirAll(filepath.Join(src, "empty"), 0700))
	must(t, os.WriteFile(filepath.Join(src, "same ü.txt"), []byte("same"), 0600))
	payload := bytes.Repeat([]byte{37}, ChunkSize+123)
	must(t, os.WriteFile(filepath.Join(src, "different.bin"), payload, 0600))
	m, e := BuildManifest(context.Background(), []string{src})
	must(t, e)
	target := filepath.Join(dest, "collection")
	must(t, os.MkdirAll(target, 0700))
	must(t, os.WriteFile(filepath.Join(target, "same ü.txt"), []byte("same"), 0600))
	must(t, os.WriteFile(filepath.Join(target, "different.bin"), []byte("old"), 0600))
	must(t, os.WriteFile(filepath.Join(target, "unrelated"), []byte("keep"), 0600))
	must(t, r.Offer(peer, m.Public()))
	v, e := r.Review(m.ID, "")
	must(t, e)
	if v.Required != int64(len(payload)) || !v.Files["collection/same ü.txt"].Identical {
		t.Fatal("incorrect conflict classification", v)
	}
	if e = r.AcceptReviewed(m.ID, "", v.Token, false); e == nil {
		t.Fatal("overwrote without explicit approval")
	}
	if s.Snapshot().Transfers[m.ID].Approved {
		t.Fatal("partial approval")
	}
	must(t, r.AcceptReviewed(m.ID, "", v.Token, true))
	var changedIndex int
	for i, en := range m.Entries {
		if en.Path == "collection/different.bin" {
			changedIndex = i
		}
	}
	b, e := ReadChunk(m.Entries[changedIndex], 0)
	must(t, e)
	must(t, r.Put(peer, m.ID, m.Digest, changedIndex, 0, b))
	// A process restart rechecks verified partials and skipped destination bytes.
	next, e := NewStore(filepath.Dir(s.path), dest)
	must(t, e)
	r = &Receiver{Store: next}
	must(t, r.Recover())
	tr := next.Snapshot().Transfers[m.ID]
	if !tr.Approved || tr.Done[changedIndex] != ChunkSize {
		t.Fatal("lost durable approval/checkpoint")
	}
	b, e = ReadChunk(m.Entries[changedIndex], ChunkSize)
	must(t, e)
	must(t, r.Put(peer, m.ID, m.Digest, changedIndex, ChunkSize, b))
	must(t, r.Finish(peer, m.ID, m.Digest))
	got, e := os.ReadFile(filepath.Join(target, "different.bin"))
	must(t, e)
	if !bytes.Equal(got, payload) {
		t.Fatal("incorrect received content")
	}
	old, e := os.ReadFile(filepath.Join(dest, backupName(m.ID), "collection", "different.bin"))
	must(t, e)
	if string(old) != "old" {
		t.Fatal("old data not preserved")
	}
	keep, e := os.ReadFile(filepath.Join(target, "unrelated"))
	must(t, e)
	if string(keep) != "keep" {
		t.Fatal("directory merge removed unrelated data")
	}
	if _, e = os.Stat(filepath.Join(target, "empty")); e != nil {
		t.Fatal("empty directory lost")
	}
}
func TestReviewedChangesLinksAndFinalizationCrash(t *testing.T) {
	s, r, dest, peer := setup(t)
	m, _ := manifest(t, []byte("replacement"))
	must(t, r.Offer(peer, m.Public()))
	file := filepath.Join(dest, m.Entries[0].Path)
	must(t, os.WriteFile(file, []byte("old"), 0600))
	v, e := r.Review(m.ID, "")
	must(t, e)
	must(t, os.WriteFile(file, []byte("edit"), 0600))
	if e = r.AcceptReviewed(m.ID, "", v.Token, true); e == nil {
		t.Fatal("stale approval token allowed overwrite")
	}
	v, e = r.Review(m.ID, "")
	must(t, e)
	must(t, r.AcceptReviewed(m.ID, "", v.Token, true))
	putAll(t, r, peer, m)
	must(t, os.WriteFile(file, []byte("later edit"), 0600))
	if e = r.Finish(peer, m.ID, m.Digest); e == nil {
		t.Fatal("changed destination overwritten")
	}
	got, _ := os.ReadFile(file)
	if string(got) != "later edit" {
		t.Fatal("local edit destroyed")
	}
	must(t, os.WriteFile(file, []byte("edit"), 0600))
	// Simulate a crash immediately after moving the original aside.
	root, e := openDestination(dest)
	must(t, e)
	defer root.Close()
	must(t, ensureDirectory(root, backupName(m.ID)))
	backup, e := root.OpenRoot(backupName(m.ID))
	must(t, e)
	defer backup.Close()
	must(t, moveReviewed(root, backup, m.Entries[0].Path, m.Entries[0].Path))
	next, e := NewStore(filepath.Dir(s.path), dest)
	must(t, e)
	r = &Receiver{Store: next}
	must(t, r.Recover())
	must(t, r.Finish(peer, m.ID, m.Digest))
	got, _ = os.ReadFile(file)
	if string(got) != "replacement" {
		t.Fatal("finalization recovery failed")
	}
	// Simulate crash after final move but before completion state commit.
	must(t, next.Update(func(st *State) error { st.Transfers[m.ID].Status = "active"; return nil }))
	must(t, r.Recover())
	must(t, r.Finish(peer, m.ID, m.Digest))
}
func TestReviewRejectsDestinationSymlinksAndIdenticalEdits(t *testing.T) {
	_, r, dest, peer := setup(t)
	m, _ := manifest(t, []byte("same"))
	must(t, r.Offer(peer, m.Public()))
	file := filepath.Join(dest, m.Entries[0].Path)
	outside := filepath.Join(t.TempDir(), "outside")
	must(t, os.WriteFile(outside, []byte("same"), 0600))
	must(t, os.Symlink(outside, file))
	if _, e := r.Review(m.ID, ""); e == nil {
		t.Fatal("symlink target accepted")
	}
	must(t, os.Remove(file))
	must(t, os.WriteFile(file, []byte("same"), 0600))
	v, e := r.Review(m.ID, "")
	must(t, e)
	must(t, r.AcceptReviewed(m.ID, "", v.Token, false))
	must(t, os.WriteFile(file, []byte("changed"), 0600))
	if e = r.Recover(); e == nil {
		t.Fatal("trusted skipped bytes without verifying")
	}
	if !r.Store.Snapshot().Transfers[m.ID].Paused {
		t.Fatal("unsafe recovery not paused")
	}
}
func TestReviewedInsufficientSpaceDoesNotApproveOrAdvance(t *testing.T) {
	s, r, _, peer := setup(t)
	m, _ := manifest(t, []byte("payload"))
	must(t, r.Offer(peer, m.Public()))
	r.space = func(string) (uint64, error) { return 0, nil }
	v, e := r.Review(m.ID, "")
	must(t, e)
	if e = r.AcceptReviewed(m.ID, "", v.Token, false); e == nil {
		t.Fatal("accepted without space")
	}
	if s.Snapshot().Transfers[m.ID].Approved {
		t.Fatal("approval committed after admission failure")
	}
	r.space = nil
	v, e = r.Review(m.ID, "")
	must(t, e)
	must(t, r.AcceptReviewed(m.ID, "", v.Token, false))
	r.space = func(string) (uint64, error) { return 0, nil }
	b, e := ReadChunk(m.Entries[0], 0)
	must(t, e)
	if e = r.Put(peer, m.ID, m.Digest, 0, 0, b); e == nil {
		t.Fatal("wrote despite full disk")
	}
	if s.Snapshot().Transfers[m.ID].Done[0] != 0 {
		t.Fatal("acknowledged unwritten bytes")
	}
}
func TestLargeSparseFileStreaming(t *testing.T) {
	if os.Getenv("HANDOFF_LARGE_TEST") != "1" {
		t.Skip("set HANDOFF_LARGE_TEST=1 for 24 GiB streaming validation")
	}
	file := filepath.Join(t.TempDir(), "large.bin")
	f, e := os.Create(file)
	must(t, e)
	must(t, f.Truncate(24<<30))
	must(t, f.Close())
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	m, e := BuildManifest(context.Background(), []string{file})
	runtime.ReadMemStats(&after)
	if after.TotalAlloc-before.TotalAlloc > 128<<20 {
		t.Fatal("large file hashing did not use bounded Go memory")
	}
	must(t, e)
	if m.Total != 24<<30 || len(m.Entries[0].Chunks) != (24<<30)/ChunkSize {
		t.Fatal("large file metadata incorrect")
	}
	b, e := ReadChunk(m.Entries[0], (24<<30)-ChunkSize)
	must(t, e)
	if len(b) != ChunkSize {
		t.Fatal("large offset truncated")
	}
}
func TestReviewedMissingPartialResetsOnlyUnverifiableProgress(t *testing.T) {
	s, r, dest, peer := setup(t)
	m, _ := manifest(t, bytes.Repeat([]byte{19}, ChunkSize+1))
	must(t, r.Offer(peer, m.Public()))
	v, e := r.Review(m.ID, "")
	must(t, e)
	must(t, r.AcceptReviewed(m.ID, "", v.Token, false))
	b, e := ReadChunk(m.Entries[0], 0)
	must(t, e)
	must(t, r.Put(peer, m.ID, m.Digest, 0, 0, b))
	must(t, os.Remove(filepath.Join(dest, stageName(m.ID), m.Entries[0].Path)))
	must(t, r.Recover())
	tr := s.Snapshot().Transfers[m.ID]
	if !tr.Approved || tr.Done[0] != 0 || tr.Paused {
		t.Fatal("missing partial did not safely reset verified progress")
	}
	putAll(t, r, peer, m)
	must(t, r.Finish(peer, m.ID, m.Digest))
}
func TestManyFilesShareHashingBuffer(t *testing.T) {
	src := filepath.Join(t.TempDir(), "collection")
	must(t, os.Mkdir(src, 0700))
	for i := 0; i < 256; i++ {
		must(t, os.WriteFile(filepath.Join(src, fmt.Sprintf("file-%03d ü.txt", i)), []byte("synthetic"), 0600))
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	m, e := BuildManifest(context.Background(), []string{src})
	must(t, e)
	runtime.ReadMemStats(&after)
	if len(m.Entries) != 257 || m.Total != 256*9 {
		t.Fatal("collection incomplete")
	}
	if after.TotalAlloc-before.TotalAlloc > 64<<20 {
		t.Fatal("allocated a large buffer per tiny file instead of reusing bounded memory")
	}
}
