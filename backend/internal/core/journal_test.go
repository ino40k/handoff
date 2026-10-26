package core

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestJournalVerifiedCheckpointAndTornTail(t *testing.T) {
	s, r, dest, peer := setup(t)
	m, _ := manifest(t, bytes.Repeat([]byte{81}, ChunkSize+1))
	if e := r.Offer(peer, m.Public()); e != nil {
		t.Fatal(e)
	}
	if e := r.Accept(m.ID, ""); e != nil {
		t.Fatal(e)
	}
	if e := s.EnableJournal(); e != nil {
		t.Fatal(e)
	}
	before, e := os.ReadFile(s.path)
	if e != nil {
		t.Fatal(e)
	}
	b, e := ReadChunk(m.Entries[0], 0)
	if e != nil {
		t.Fatal(e)
	}
	if e = r.Put(peer, m.ID, m.Digest, 0, 0, b); e != nil {
		t.Fatal(e)
	}
	after, _ := os.ReadFile(s.path)
	if !bytes.Equal(before, after) {
		t.Fatal("checkpoint rewrote complete manifest")
	}
	journal, e := os.ReadFile(s.journalPath())
	if e != nil || len(journal) == 0 {
		t.Fatal("checkpoint not journaled", e)
	}
	if bytes.Contains(journal, []byte(m.Entries[0].Hash)) || bytes.Contains(journal, []byte(m.Entries[0].Source)) {
		t.Fatal("checkpoint duplicated immutable manifest/source")
	}
	// An incomplete append after the durable record must not discard that record.
	f, e := os.OpenFile(s.journalPath(), os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	f.WriteString(`{"unacknowledged":`)
	f.Close()
	next, e := NewStore(filepath.Dir(s.path), dest)
	if e != nil {
		t.Fatal(e)
	}
	if next.Snapshot().Transfers[m.ID].Done[0] != ChunkSize || !next.Snapshot().Transfers[m.ID].Approved {
		t.Fatal("lost checkpoint/approval")
	}
	if e = next.EnableJournal(); e != nil {
		t.Fatal(e)
	}
	if e = next.Update(func(st *State) error { st.Transfers[m.ID].Paused = true; return nil }); e != nil {
		t.Fatal(e)
	}
	reopened, e := NewStore(filepath.Dir(s.path), dest)
	if e != nil {
		t.Fatal(e)
	}
	if !reopened.Snapshot().Transfers[m.ID].Paused {
		t.Fatal("did not discard torn tail before next append")
	}
}
func TestJournalCompactionRevisionAndCorruption(t *testing.T) {
	s, r, dest, peer := setup(t)
	m, _ := manifest(t, []byte("content"))
	if e := r.Offer(peer, m.Public()); e != nil {
		t.Fatal(e)
	}
	if e := s.EnableJournal(); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 300; i++ {
		if e := s.Update(func(st *State) error { st.Transfers[m.ID].Order++; return nil }); e != nil {
			t.Fatal(e)
		}
	}
	if s.journalRecords >= 256 {
		t.Fatal("journal not compacted")
	}
	reopened, e := NewStore(filepath.Dir(s.path), dest)
	if e != nil {
		t.Fatal(e)
	}
	if reopened.Snapshot().Transfers[m.ID].Order != s.Snapshot().Transfers[m.ID].Order {
		t.Fatal("compaction changed queue")
	}
	// Crash after a newer atomic checkpoint but before truncating the previous WAL.
	newer := s.Snapshot()
	newer.Revision++
	newer.Transfers[m.ID].Order++
	if e = s.write(newer); e != nil {
		t.Fatal(e)
	}
	reopened, e = NewStore(filepath.Dir(s.path), dest)
	if e != nil {
		t.Fatal(e)
	}
	if reopened.Snapshot().Transfers[m.ID].Order != newer.Transfers[m.ID].Order {
		t.Fatal("old WAL replayed over newer snapshot")
	}
	f, e := os.OpenFile(s.journalPath(), os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	json.NewEncoder(f).Encode(journalRecord{Payload: json.RawMessage(`{"revision":999}`), Checksum: "invalid"})
	f.Close()
	if _, e = NewStore(filepath.Dir(s.path), dest); e == nil {
		t.Fatal("corrupt journal silently ignored")
	}
}
func TestSnapshotDeepIsolation(t *testing.T) {
	s, r, _, peer := setup(t)
	m, _ := manifest(t, []byte("x"))
	if e := r.Offer(peer, m.Public()); e != nil {
		t.Fatal(e)
	}
	snapshot := s.Snapshot()
	snapshot.Peers[peer].Name = "changed"
	snapshot.Transfers[m.ID].Manifest.Entries[0].Chunks[0] = "changed"
	snapshot.Transfers[m.ID].Done[0] = 1
	actual := s.Snapshot()
	if actual.Peers[peer].Name == "changed" || actual.Transfers[m.ID].Manifest.Entries[0].Chunks[0] == "changed" || actual.Transfers[m.ID].Done[0] != 0 {
		t.Fatal("mutable snapshot escaped transaction boundary")
	}
}
func TestMissingSnapshotDoesNotReplaceExistingIdentity(t *testing.T) {
	dir := t.TempDir()
	if e := os.WriteFile(filepath.Join(dir, "progress.wal"), nil, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := NewStore(dir, dir); e == nil {
		t.Fatal("generated a new identity alongside existing durable progress")
	}
	if _, e := os.Stat(filepath.Join(dir, "state.json")); !os.IsNotExist(e) {
		t.Fatal("wrote replacement identity")
	}
}
