package core

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/tailscale/tailcat"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
	"time"
)

const ChunkSize = 4 << 20
const MaxEntries = 100000
const MaxManifest = 32 << 20
const Version = "1.0.7"

type Settings struct {
	Name       string `json:"name"`
	ReceiveDir string `json:"receiveDir"`
	Theme      string `json:"theme"`
	Autostart  bool   `json:"autostart"`
	Configured bool   `json:"configured"`
}
type Identity struct {
	Key    key.NodePrivate      `json:"key"`
	PSK    tailcat.PresharedKey `json:"psk"`
	Region *tailcfg.DERPRegion  `json:"region,omitempty"`
}
type Peer struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Address   tailcat.Addr    `json:"address"`
	ServerKey key.NodePublic  `json:"serverKey"`
	RemoteKey key.NodePublic  `json:"remoteKey"`
	LocalKey  key.NodePrivate `json:"localKey"`
}
type Entry struct {
	Path   string   `json:"path"`
	Dir    bool     `json:"dir,omitempty"`
	Size   int64    `json:"size"`
	Hash   string   `json:"hash,omitempty"`
	Chunks []string `json:"chunks,omitempty"`
	Source string   `json:"source,omitempty"`
}
type Manifest struct {
	ID      string  `json:"id"`
	Digest  string  `json:"digest"`
	Entries []Entry `json:"entries"`
	Total   int64   `json:"total"`
}
type Transfer struct {
	ID          string            `json:"id"`
	PeerID      string            `json:"peerId"`
	Direction   string            `json:"direction"`
	Manifest    Manifest          `json:"manifest"`
	Status      string            `json:"status"`
	Approved    bool              `json:"approved"`
	Offered     bool              `json:"offered"`
	RemoteAck   bool              `json:"remoteAck"`
	Paused      bool              `json:"paused"`
	Destination string            `json:"destination,omitempty"`
	Done        []int64           `json:"done"`
	Final       map[string]string `json:"final,omitempty"`
	Error       string            `json:"error,omitempty"`
	Created     time.Time         `json:"created"`
	Updated     time.Time         `json:"updated"`
	Order       int64             `json:"order"`
}
type Receipt struct {
	PeerID   string `json:"peerId"`
	Digest   string `json:"digest"`
	Status   string `json:"status"`
	Approved bool   `json:"approved"`
}
type State struct {
	Receipts  map[string]Receipt   `json:"receipts,omitempty"`
	Schema    int                  `json:"schema"`
	Identity  Identity             `json:"identity"`
	Settings  Settings             `json:"settings"`
	Peers     map[string]*Peer     `json:"peers"`
	Transfers map[string]*Transfer `json:"transfers"`
}

func RandomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func PeerID(k key.NodePublic) string {
	b, _ := k.MarshalText()
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:16])
}
func (m Manifest) Public() Manifest {
	m.Entries = append([]Entry(nil), m.Entries...)
	for i := range m.Entries {
		m.Entries[i].Source = ""
	}
	return m
}
func (m Manifest) CalculateDigest() string {
	m = m.Public()
	m.Digest = ""
	b, _ := json.Marshal(m)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func ValidateID(id string) bool { b, e := hex.DecodeString(id); return e == nil && len(b) == 16 }
func Terminal(s string) bool    { return s == "completed" || s == "declined" || s == "canceled" }
func ValidateManifest(m Manifest) error {
	if !ValidateID(m.ID) || len(m.Entries) == 0 || len(m.Entries) > MaxEntries || m.Digest != m.CalculateDigest() {
		return errors.New("invalid manifest identity or digest")
	}
	seen := map[string]bool{}
	dirs := map[string]bool{}
	var total int64
	for _, e := range m.Entries {
		p, err := ValidatePath(e.Path)
		if err != nil {
			return err
		}
		if seen[p] {
			return fmt.Errorf("duplicate or case-conflicting path: %s", e.Path)
		}
		seen[p] = true
		if e.Source != "" {
			return errors.New("remote manifest contains source path")
		}
		if e.Dir {
			if e.Size != 0 || e.Hash != "" || len(e.Chunks) != 0 {
				return errors.New("invalid directory")
			}
			dirs[p] = true
			continue
		}
		if e.Size < 0 || e.Size > 1<<50 || len(e.Hash) != 64 || int64(len(e.Chunks)) != (e.Size+ChunkSize-1)/ChunkSize {
			return errors.New("invalid file metadata")
		}
		if _, err := hex.DecodeString(e.Hash); err != nil {
			return errors.New("invalid file hash")
		}
		for _, h := range e.Chunks {
			if b, err := hex.DecodeString(h); err != nil || len(b) != 32 {
				return errors.New("invalid chunk hash")
			}
		}
		if total > 1<<60-e.Size {
			return errors.New("transfer too large")
		}
		total += e.Size
	}
	for p := range seen {
		for parent := parentPath(p); parent != ""; parent = parentPath(parent) {
			if !dirs[parent] {
				return errors.New("missing parent directory in manifest")
			}
		}
	}
	if total != m.Total {
		return errors.New("incorrect total size")
	}
	return nil
}
