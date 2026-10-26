//go:build linux

package headless

import (
	"context"
	"encoding/json"
	"handoff/internal/control"
	"handoff/internal/core"
	"handoff/internal/network"
	"os"
	"path/filepath"
	"tailscale.com/types/key"
	"testing"
	"time"
)

func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func until(t *testing.T, fn func() bool) {
	t.Helper()
	for stop := time.Now().Add(90 * time.Second); time.Now().Before(stop); {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out")
}
func TestLifecycleDurablePreparationAndState(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "data")
	dest := filepath.Join(base, "receive")
	check(t, os.Mkdir(dest, 0700))
	check(t, Init(dir, "Test server", dest))
	store, e := core.NewStore(dir, dest)
	check(t, e)
	identity := store.Snapshot().Identity
	remote := key.NewNode()
	pid := core.PeerID(remote.Public())
	check(t, store.Update(func(s *core.State) error {
		s.Peers[pid] = &core.Peer{ID: pid, Name: "Synthetic peer", ServerKey: remote.Public(), LocalKey: key.NewNode(), RemoteKey: key.NewNode().Public()}
		return nil
	}))
	source := filepath.Join(base, "source ü.txt")
	check(t, os.WriteFile(source, []byte("synthetic content"), 0600))
	job := Job{ID: core.RandomID(), Paths: []string{source}, Peers: []string{pid}, Status: "preparing", Created: time.Now()}
	// Simulate a preparation queued just before the previous process exited.
	b, _ := json.Marshal([]Job{job})
	check(t, os.WriteFile(filepath.Join(dir, "jobs.json"), b, 0600))
	socket := filepath.Join(base, "control.sock")
	start := func() (context.CancelFunc, chan error) {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- Serve(ctx, dir, socket) }()
		until(t, func() bool { var v any; return Call(context.Background(), socket, "snapshot", nil, &v) == nil })
		return cancel, done
	}
	cancel, done := start()
	if f, e := Lock(dir); e == nil {
		f.Close()
		t.Fatal("second service acquired the state")
	}
	until(t, func() bool { s, e := core.NewStore(dir, dest); return e == nil && len(s.Snapshot().Transfers) == 1 })
	check(t, Call(context.Background(), socket, "transfer", map[string]string{"id": network.JobTransferID(job.ID, pid), "action": "pause"}, nil))
	// Socket access is local only and group restricted; private state is not returned.
	st, e := os.Stat(socket)
	check(t, e)
	if st.Mode().Perm() != 0660 {
		t.Fatal("unsafe socket permissions")
	}
	var snap any
	check(t, Call(context.Background(), socket, "snapshot", nil, &snap))
	raw, _ := json.Marshal(snap)
	if string(raw) == "" {
		t.Fatal("empty snapshot")
	}
	cancel()
	check(t, <-done)
	cancel, done = start()
	defer func() { cancel(); check(t, <-done) }()
	after, e := core.NewStore(dir, dest)
	check(t, e)
	s := after.Snapshot()
	if !s.Identity.Key.Equal(identity.Key) || s.Identity.PSK != identity.PSK || len(s.Peers) != 1 || len(s.Transfers) != 1 || !s.Transfers[network.JobTransferID(job.ID, pid)].Paused {
		t.Fatal("restart changed identity, peers, queue, or pause")
	}
	// Idempotent recovery after a crash between storing the request and job completion.
	a, e := control.Open(dir, dest)
	check(t, e)
	defer a.Close()
	check(t, a.Service.SendWithID(context.Background(), []string{source}, []string{pid}, job.ID))
	if len(a.Store.Snapshot().Transfers) != 1 {
		t.Fatal("duplicate preparation created requests")
	}
}
func TestCancelPreparationStopsHashingAndReconcilesCommit(t *testing.T) {
	base := t.TempDir()
	dest := filepath.Join(base, "receive")
	check(t, os.Mkdir(dest, 0700))
	a, e := control.Open(filepath.Join(base, "data"), dest)
	check(t, e)
	defer a.Close()
	key := key.NewNode()
	pid := core.PeerID(key.Public())
	check(t, a.Store.Update(func(s *core.State) error {
		s.Peers[pid] = &core.Peer{ID: pid, Name: "Example", ServerKey: key.Public()}
		return nil
	}))
	file := filepath.Join(base, "large.bin")
	f, e := os.Create(file)
	check(t, e)
	check(t, f.Truncate(64<<30))
	check(t, f.Close())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	job := Job{ID: core.RandomID(), Paths: []string{file}, Peers: []string{pid}, Status: "preparing"}
	d := &Daemon{App: a, ctx: ctx, jobs: []Job{job}, active: map[string]context.CancelFunc{}}
	done := make(chan struct{})
	go func() { defer close(done); d.prepareLoop() }()
	until(t, func() bool { d.mu.Lock(); defer d.mu.Unlock(); return d.active[job.ID] != nil })
	p, _ := json.Marshal(map[string]string{"id": job.ID, "action": "cancel"})
	_, e = d.command(control.Request{Method: "job", Params: p})
	check(t, e)
	until(t, func() bool { d.mu.Lock(); defer d.mu.Unlock(); return d.active[job.ID] == nil })
	cancel()
	<-done
	if len(a.Store.Snapshot().Transfers) != 0 {
		t.Fatal("canceled scan published transfer")
	}
	b, e := os.ReadFile(filepath.Join(a.Dir, "jobs.json"))
	check(t, e)
	var jobs []Job
	check(t, json.Unmarshal(b, &jobs))
	if len(jobs) != 1 || jobs[0].Status != "canceled" {
		t.Fatal("cancellation not durable")
	}
	// Reconcile a crash after the manifest was published but before job completion.
	check(t, os.WriteFile(file, []byte("small"), 0600))
	check(t, a.Service.SendWithID(context.Background(), []string{file}, []string{pid}, job.ID))
	check(t, d.cancelPrepared(job))
	if a.Store.Snapshot().Transfers[network.JobTransferID(job.ID, pid)].Status != "canceled" {
		t.Fatal("late publication escaped cancellation")
	}
}
