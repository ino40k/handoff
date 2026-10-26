//go:build linux

package headless

import (
	"bytes"
	"context"
	"handoff/internal/core"
	"os"
	"path/filepath"
	"testing"
)

func TestHeadlessPeersPendingAndAcceptedRestart(t *testing.T) {
	if os.Getenv("HANDOFF_NETWORK_TEST") != "1" {
		t.Skip("real CLI peers and interrupted tailcat transfers")
	}
	type host struct {
		dir, dest, socket string
		cancel            context.CancelFunc
		done              chan error
	}
	create := func(name string) *host {
		base := t.TempDir()
		h := &host{dir: filepath.Join(base, "data"), dest: filepath.Join(base, "receive"), socket: filepath.Join(base, "control.sock")}
		check(t, os.Mkdir(h.dest, 0700))
		check(t, Init(h.dir, name, h.dest))
		return h
	}
	start := func(h *host) {
		ctx, cancel := context.WithCancel(context.Background())
		h.cancel = cancel
		h.done = make(chan error, 1)
		go func() { h.done <- Serve(ctx, h.dir, h.socket) }()
		until(t, func() bool {
			var v struct{ Ready bool }
			return Call(context.Background(), h.socket, "snapshot", nil, &v) == nil && v.Ready
		})
	}
	stop := func(h *host) {
		if h.cancel != nil {
			h.cancel()
			check(t, <-h.done)
			h.cancel = nil
		}
	}
	a, b := create("Sender example"), create("Receiver example")
	defer stop(a)
	defer stop(b)
	start(a)
	start(b)
	call := func(h *host, method string, p, out any) {
		t.Helper()
		check(t, Call(context.Background(), h.socket, method, p, out))
	}
	type view struct {
		Peers []struct {
			ID     string
			Online bool
		}
		Pairings  []struct{ ID, Status string }
		Transfers []struct {
			ID, Status string
			Done, Size int64
			Approved   bool
		}
	}
	snap := func(h *host) view { var v view; call(h, "snapshot", nil, &v); return v }
	var invite map[string]string
	call(a, "invite", nil, &invite)
	call(b, "join", map[string]string{"code": invite["code"]}, nil)
	var pair string
	until(t, func() bool {
		for _, p := range snap(a).Pairings {
			if p.Status == "approval" {
				pair = p.ID
				return true
			}
		}
		return false
	})
	call(a, "pairDecision", map[string]any{"id": pair, "approve": true}, nil)
	until(t, func() bool {
		v := snap(a)
		w := snap(b)
		return len(v.Peers) == 1 && len(w.Peers) == 1 && v.Peers[0].Online && w.Peers[0].Online
	})
	source := filepath.Join(t.TempDir(), "stream.bin")
	f, e := os.Create(source)
	check(t, e)
	block := bytes.Repeat([]byte("synthetic"), core.ChunkSize/9+1)
	for i := 0; i < 16; i++ {
		_, e = f.Write(block)
		check(t, e)
	}
	check(t, f.Close())
	var job Job
	call(a, "send", map[string]any{"paths": []string{source}, "peers": []string{snap(a).Peers[0].ID}}, &job)
	var id string
	until(t, func() bool {
		for _, tr := range snap(b).Transfers {
			if tr.Status == "pending" {
				id = tr.ID
				if tr.Done != 0 {
					t.Fatal("bytes before approval")
				}
				return true
			}
		}
		return false
	})
	// Restart while unanswered; the request and identity must remain unchanged.
	before, e := core.NewStore(b.dir, b.dest)
	check(t, e)
	identity := before.Snapshot().Identity
	stop(b)
	start(b)
	found := false
	for _, tr := range snap(b).Transfers {
		if tr.ID == id && tr.Status == "pending" && !tr.Approved {
			found = true
		}
	}
	if !found {
		t.Fatal("pending request lost or automatically accepted")
	}
	var review core.ConflictReview
	call(b, "review", map[string]string{"id": id}, &review)
	call(b, "acceptReviewed", map[string]string{"id": id, "token": review.Token}, nil)
	var checkpoint int64
	until(t, func() bool {
		for _, tr := range snap(b).Transfers {
			if tr.ID == id && tr.Done >= core.ChunkSize && tr.Done < tr.Size {
				checkpoint = tr.Done
				return true
			}
		}
		return false
	})
	stop(b)
	start(b)
	after, e := core.NewStore(b.dir, b.dest)
	check(t, e)
	saved := after.Snapshot()
	if !saved.Identity.Key.Equal(identity.Key) || saved.Identity.PSK != identity.PSK || !saved.Transfers[id].Approved || saved.Transfers[id].Done[0] < checkpoint {
		t.Fatal("restart lost identity, approval or verified progress")
	}
	until(t, func() bool {
		for _, tr := range snap(b).Transfers {
			if tr.ID == id && tr.Status == "completed" {
				return true
			}
		}
		return false
	})
	until(t, func() bool {
		for _, tr := range snap(a).Transfers {
			if tr.ID == id && tr.Status == "completed" {
				return true
			}
		}
		return false
	})
	actual, e := core.BuildManifest(context.Background(), []string{filepath.Join(b.dest, "stream.bin")})
	check(t, e)
	expected, e := core.BuildManifest(context.Background(), []string{source})
	check(t, e)
	if actual.Entries[0].Hash != expected.Entries[0].Hash {
		t.Fatal("resumed headless transfer integrity mismatch")
	}
	call(b, "removeRecord", map[string]string{"id": id}, nil)
	if len(snap(b).Transfers) != 0 {
		t.Fatal("terminal record retained")
	}
	t.Logf("CLI -> CLI resumed automatically after a verified %d-byte checkpoint; pending approval and terminal receipt retained", checkpoint)
}
