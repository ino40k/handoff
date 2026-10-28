package core

import "errors"

// Include terminal history and replay receipts: canceling or clearing history
// must not reset a remote sender's resource accounting. Existing IDs remain
// replayable even when a profile has reached these new-request limits.
const (
	maxPeerRequestMetadata = 64 << 20
	maxRequestMetadata     = 128 << 20
	maxPeerRequestRecords  = 65536
	maxRequestRecords      = 262144
)

func manifestMemory(m Manifest) int64 {
	n := int64(512)
	for _, e := range m.Entries {
		n += int64(256 + len(e.Path) + len(e.Hash) + len(e.Source))
		for _, hash := range e.Chunks {
			n += int64(16 + len(hash))
		}
	}
	return n
}

func checkOfferBudget(s *State, peer string, m Manifest) error {
	peerBytes, totalBytes := manifestMemory(m), manifestMemory(m)
	peerRecords, totalRecords := 1, 1
	add := func(owner string, size int64) {
		totalBytes += size
		totalRecords++
		if owner == peer {
			peerBytes += size
			peerRecords++
		}
	}
	for _, t := range s.Transfers {
		if t.Direction == "incoming" {
			add(t.PeerID, manifestMemory(t.Manifest))
		}
	}
	for _, receipt := range s.Receipts {
		add(receipt.PeerID, int64(128+len(receipt.PeerID)+len(receipt.Digest)+len(receipt.Status)))
	}
	if peerBytes > maxPeerRequestMetadata || totalBytes > maxRequestMetadata {
		return errors.New("incoming request metadata limit reached; clear finished transfer history to free manifest storage")
	}
	if peerRecords > maxPeerRequestRecords || totalRecords > maxRequestRecords {
		return errors.New("incoming request replay-record limit reached; new requests are disabled for this profile")
	}
	return nil
}
