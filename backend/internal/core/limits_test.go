package core

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanceledOffersCannotGrowMetadataWithoutBound(t *testing.T) {
	s, r, dest, peer := setup(t)
	// A remote manifest can describe a large source without sending its bytes.
	chunks := make([]string, 200000)
	for i := range chunks {
		chunks[i] = strings.Repeat("0", 64)
	}
	m := Manifest{Entries: []Entry{{Path: "large.bin", Size: int64(len(chunks)) * ChunkSize, Hash: strings.Repeat("0", 64), Chunks: chunks}}}
	m.Total = m.Entries[0].Size
	var saved Manifest
	rejected := false
	for i := 0; i < 8; i++ {
		m.ID = RandomID()
		m.Digest = m.CalculateDigest()
		if err := r.Offer(peer, m); err != nil {
			if !strings.Contains(err.Error(), "metadata limit") {
				t.Fatal(err)
			}
			rejected = true
			break
		}
		saved = m
		if err := r.Change(m.ID, "cancel"); err != nil {
			t.Fatal(err)
		}
	}
	if !rejected || saved.ID == "" {
		t.Fatal("offer/cancel churn was not bounded")
	}
	if err := r.Offer(peer, saved); err != nil {
		t.Fatalf("existing request replay must work at quota: %v", err)
	}
	reopened, err := NewStore(filepath.Dir(s.path), dest)
	if err != nil {
		t.Fatal(err)
	}
	if err = (&Receiver{Store: reopened}).Offer(peer, m); err == nil {
		t.Fatal("restart reset retained metadata accounting")
	}
}

func TestOfferBudgetsIncludeReceiptsAndOtherPeers(t *testing.T) {
	m := Manifest{Entries: []Entry{{Path: "empty", Hash: strings.Repeat("0", 64)}}}
	s := State{Transfers: map[string]*Transfer{}, Receipts: map[string]Receipt{}}
	for i := 0; i < maxPeerRequestRecords; i++ {
		s.Receipts[fmt.Sprintf("%032x", i)] = Receipt{PeerID: "peer", Digest: strings.Repeat("0", 64), Status: "canceled"}
	}
	if err := checkOfferBudget(&s, "peer", m); err == nil {
		t.Fatal("history compaction reset request accounting")
	}
	if err := checkOfferBudget(&s, "other", m); err != nil {
		t.Fatalf("one peer's record quota blocked another: %v", err)
	}
	// Multiple peers cannot bypass the aggregate metadata cap.
	large := Manifest{Entries: []Entry{{Path: strings.Repeat("x", maxRequestMetadata/2)}}}
	s.Transfers["a"] = &Transfer{PeerID: "a", Direction: "incoming", Manifest: large, Status: "completed"}
	s.Transfers["b"] = &Transfer{PeerID: "b", Direction: "incoming", Manifest: large, Status: "declined"}
	if err := checkOfferBudget(&s, "other", m); err == nil {
		t.Fatal("other peers bypassed global retained metadata budget")
	}
}
