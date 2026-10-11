package network

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/tailscale/tailcat"
	"handoff/internal/core"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"tailscale.com/types/key"
	"testing"
	"time"
)

func testService(t *testing.T) *Service {
	t.Helper()
	dir := t.TempDir()
	dest := filepath.Join(dir, "received")
	os.Mkdir(dest, 0700)
	st, e := core.NewStore(dir, dest)
	if e != nil {
		t.Fatal(e)
	}
	log, e := core.NewLogger(filepath.Join(dir, "logs"))
	if e != nil {
		t.Fatal(e)
	}
	s := New(st, log, dir)
	t.Cleanup(func() { s.Close(); log.Close() })
	return s
}
func testPeer(t *testing.T, s *Service) *core.Peer {
	t.Helper()
	k := key.NewNode()
	p := &core.Peer{ID: core.PeerID(k.Public()), Name: "peer", ServerKey: k.Public(), LocalKey: key.NewNode(), RemoteKey: key.NewNode().Public()}
	s.Store.Update(func(st *core.State) error { st.Peers[p.ID] = p; return nil })
	s.allow.Add(p.RemoteKey)
	return p
}
func TestInvitationExpirationAndAddressPolicy(t *testing.T) {
	k := key.NewNode()
	ci := &tailcat.ConnInfo{ServerPublic: tailcat.NodePublic{NodePublic: k.Public()}, ServerDiscoPublic: tailcat.DiscoPublicForNode(k), PresharedKey: tailcat.NewPresharedKey(), RegionID: 301}
	c := inviteCode{1, "test", ci.Addr(), core.RandomID() + core.RandomID(), time.Now().Add(15 * time.Minute)}
	encode := func() string { b, _ := json.Marshal(c); return "handoff1." + base64.RawURLEncoding.EncodeToString(b) }
	if _, e := ParseInvitation(encode()); e != nil {
		t.Fatal(e)
	}
	c.Expires = time.Now().Add(-time.Second)
	if _, e := ParseInvitation(encode()); e == nil {
		t.Fatal("expired code accepted")
	}
	ci.PresharedKey = tailcat.PresharedKey{}
	if _, e := ValidateAddress(ci.Addr()); e == nil {
		t.Fatal("unprotected address accepted")
	}
}
func TestAuthenticationAndRemoval(t *testing.T) {
	s := testService(t)
	p := testPeer(t, s)
	request := func(k *key.NodePublic) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/hello", bytes.NewReader([]byte(`{"version":1}`)))
		if k != nil {
			r = r.WithContext(context.WithValue(r.Context(), peerContext{}, *k))
		}
		w := httptest.NewRecorder()
		s.handle(w, r)
		return w
	}
	if request(nil).Code != 403 {
		t.Fatal("anonymous access")
	}
	wrong := key.NewNode().Public()
	if request(&wrong).Code != 403 {
		t.Fatal("stranger access")
	}
	if request(&p.RemoteKey).Code != 200 {
		t.Fatal("trusted authentication failed")
	}
	if e := s.RemovePeer(p.ID); e != nil {
		t.Fatal(e)
	}
	if s.allow.Contains(p.RemoteKey) || request(&p.RemoteKey).Code != 403 {
		t.Fatal("removed key still has access")
	}
}
func TestQueueOrderingAndHistory(t *testing.T) {
	s := testService(t)
	p := testPeer(t, s)
	ids := []string{core.RandomID(), core.RandomID(), core.RandomID()}
	s.Store.Update(func(st *core.State) error {
		for i, id := range ids {
			st.Transfers[id] = &core.Transfer{ID: id, PeerID: p.ID, Direction: "outgoing", Status: "queued", Order: int64(i + 1)}
		}
		return nil
	})
	if e := s.Reorder(ids[2], -1); e != nil {
		t.Fatal(e)
	}
	st := s.Store.Snapshot()
	if st.Transfers[ids[2]].Order != 2 || st.Transfers[ids[1]].Order != 3 {
		t.Fatal("queue move failed")
	}
	s.Store.Update(func(st *core.State) error { st.Transfers[ids[1]].Status = "completed"; return nil })
	s.ClearHistory()
	if s.Store.Snapshot().Transfers[ids[0]] == nil || s.Store.Snapshot().Transfers[ids[1]] != nil {
		t.Fatal("history clearing removed active queue")
	}
}
func waitFor(t *testing.T, label string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(80 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatal("timed out: " + label)
}
func TestLiveTailcatPairingAndTransfer(t *testing.T) {
	if os.Getenv("HANDOFF_NETWORK_TEST") != "1" {
		t.Skip("set HANDOFF_NETWORK_TEST=1 for a real public-DERP/tailcat integration test")
	}
	a, b := testService(t), testService(t)
	go a.Run()
	go b.Run()
	waitFor(t, "listeners", func() bool { x, _ := a.Ready(); y, _ := b.Ready(); return x && y })
	code, e := a.CreateInvitation()
	if e != nil {
		t.Fatal(e)
	}
	if e = b.JoinInvitation(code); e != nil {
		t.Fatal(e)
	}
	var pairID string
	waitFor(t, "pair approval", func() bool {
		for _, p := range a.Pairings() {
			if p.Status == "approval" {
				pairID = p.ID
				return true
			}
		}
		return false
	})
	if len(a.Store.Snapshot().Peers) != 0 {
		t.Fatal("paired without inviter approval")
	}
	if e = a.PairDecision(pairID, true); e != nil {
		t.Fatal(e)
	}
	waitFor(t, "paired", func() bool { return len(a.Store.Snapshot().Peers) == 1 && len(b.Store.Snapshot().Peers) == 1 })
	var bid string
	for id := range a.Store.Snapshot().Peers {
		bid = id
	}
	src := filepath.Join(t.TempDir(), "proof.bin")
	content := bytes.Repeat([]byte("handoff-tailcat-proof"), 8192)
	os.WriteFile(src, content, 0600)
	if e = a.Send(context.Background(), []string{src}, []string{bid}); e != nil {
		t.Fatal(e)
	}
	var tid string
	waitFor(t, "incoming pending", func() bool {
		for id, tr := range b.Store.Snapshot().Transfers {
			if tr.Status == "pending" {
				tid = id
				return true
			}
		}
		return false
	})
	time.Sleep(time.Second)
	if b.Store.Snapshot().Transfers[tid].Done[0] != 0 {
		t.Fatal("bytes sent before approval")
	}
	if e = b.LocalAction(tid, "accept", ""); e != nil {
		t.Fatal(e)
	}
	waitFor(t, "received", func() bool { return b.Store.Snapshot().Transfers[tid].Status == "completed" })
	waitFor(t, "sender complete", func() bool { return a.Store.Snapshot().Transfers[tid].Status == "completed" })
	got, e := os.ReadFile(filepath.Join(b.Store.Snapshot().Settings.ReceiveDir, "proof.bin"))
	if e != nil || !bytes.Equal(got, content) {
		t.Fatal("tailcat integrity failure", e)
	}
	p := a.Store.Snapshot().Peers[bid]
	pr := a.Presence()[bid]
	t.Logf("tailcat path=%s online=%v latency=%.2fms", pr.Path, pr.Online, pr.Latency)
	if !pr.Online {
		t.Fatal("presence inaccurate")
	}
	if e = a.RemovePeer(bid); e != nil {
		t.Fatal(e)
	}
	if a.allow.Contains(p.RemoteKey) {
		t.Fatal("key remains allowed")
	}
}

func TestLiveRestartResume(t *testing.T) {
	if os.Getenv("HANDOFF_NETWORK_TEST") != "1" {
		t.Skip("real tailcat restart test")
	}
	a, b := testService(t), testService(t)
	// Run the real transport with a manually stepped sender, so interruption is
	// deterministic at exactly one verified block.
	for _, s := range []*Service{a, b} {
		if e := s.start(); e != nil {
			t.Fatal(e)
		}
		s.ready = true
		go s.presenceLoop()
	}
	code, e := a.CreateInvitation()
	if e != nil {
		t.Fatal(e)
	}
	if e = b.JoinInvitation(code); e != nil {
		t.Fatal(e)
	}
	var pair string
	waitFor(t, "approval", func() bool {
		for _, p := range a.Pairings() {
			if p.Status == "approval" {
				pair = p.ID
				return true
			}
		}
		return false
	})
	a.PairDecision(pair, true)
	waitFor(t, "paired", func() bool { return len(b.Store.Snapshot().Peers) == 1 })
	var pid string
	for id := range a.Store.Snapshot().Peers {
		pid = id
	}
	p := a.Store.Snapshot().Peers[pid]
	src := filepath.Join(t.TempDir(), "resume.bin")
	content := bytes.Repeat([]byte{73}, core.ChunkSize+100)
	os.WriteFile(src, content, 0600)
	if e = a.Send(context.Background(), []string{src}, []string{pid}); e != nil {
		t.Fatal(e)
	}
	var tid string
	for id := range a.Store.Snapshot().Transfers {
		tid = id
	}
	a.syncTransfer(context.Background(), p, a.Store.Snapshot().Transfers[tid])
	if e = b.LocalAction(tid, "accept", ""); e != nil {
		t.Fatal(e)
	}
	if e = a.sendNext(p, a.Store.Snapshot().Transfers[tid]); e != nil {
		t.Fatal(e)
	}
	if b.Store.Snapshot().Transfers[tid].Done[0] != core.ChunkSize {
		t.Fatal("checkpoint not durable")
	}
	// Restart the receiver only. The existing sender must recreate the tailcat
	// registration rather than relying on the client's first-use upDone flag.
	bdir := b.cache.Dir
	a.mu.Lock()
	oldClient := a.clients[pid]
	a.mu.Unlock()
	b.Close()
	newStore, e := core.NewStore(bdir, b.Store.Snapshot().Settings.ReceiveDir)
	if e != nil {
		t.Fatal(e)
	}
	restarted := New(newStore, b.Log, bdir)
	t.Cleanup(restarted.Close)
	go restarted.Run()
	waitFor(t, "receiver restarted", func() bool { ready, _ := restarted.Ready(); return ready })
	waitFor(t, "automatic reconnection", func() bool {
		a.mu.Lock()
		current := a.clients[pid]
		a.mu.Unlock()
		return current != nil && current != oldClient && a.Presence()[pid].Online
	})
	if e = a.sendNext(p, a.Store.Snapshot().Transfers[tid]); e != nil {
		t.Fatal(e)
	}
	if !restarted.Store.Snapshot().Transfers[tid].Approved {
		t.Fatal("approval lost")
	}
	// Restart the sender too; its automatic queue resumes the accepted request.
	adir := a.cache.Dir
	a.Close()
	as, e := core.NewStore(adir, a.Store.Snapshot().Settings.ReceiveDir)
	if e != nil {
		t.Fatal(e)
	}
	sender := New(as, a.Log, adir)
	t.Cleanup(sender.Close)
	go sender.Run()
	waitFor(t, "restart completion", func() bool {
		return restarted.Store.Snapshot().Transfers[tid].Status == "completed" && sender.Store.Snapshot().Transfers[tid].Status == "completed"
	})
	got, e := os.ReadFile(filepath.Join(restarted.Store.Snapshot().Settings.ReceiveDir, "resume.bin"))
	if e != nil || !bytes.Equal(got, content) {
		t.Fatal("restart integrity", e)
	}
}
func TestClearedHistoryRetainsAuthenticatedReceipt(t *testing.T) {
	s := testService(t)
	p := testPeer(t, s)
	src := filepath.Join(t.TempDir(), "file")
	os.WriteFile(src, []byte("ok"), 0600)
	m, e := core.BuildManifest(context.Background(), []string{src})
	if e != nil {
		t.Fatal(e)
	}
	s.Receiver.Offer(p.ID, m.Public())
	s.Receiver.Accept(m.ID, "")
	data, _ := core.ReadChunk(m.Entries[0], 0)
	s.Receiver.Put(p.ID, m.ID, m.Digest, 0, 0, data)
	if e = s.Receiver.Finish(p.ID, m.ID, m.Digest); e != nil {
		t.Fatal(e)
	}
	s.ClearHistory()
	if s.Store.Snapshot().Transfers[m.ID] != nil {
		t.Fatal("history remains visible")
	}
	status := s.transferStatus(p.ID, m.ID, m.Digest)
	if status == nil || status.Status != "completed" {
		t.Fatal("offline sender lost completion receipt")
	}
	if s.transferStatus("other", m.ID, m.Digest) != nil {
		t.Fatal("receipt readable by another peer")
	}
	if e = s.Receiver.Offer(p.ID, m.Public()); e != nil {
		t.Fatal(e)
	}
	if s.Store.Snapshot().Transfers[m.ID] != nil {
		t.Fatal("replay reopened a request")
	}
}
