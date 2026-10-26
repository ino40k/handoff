package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/tailscale/tailcat"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"tailscale.com/types/key"
)

type Store struct {
	mu             sync.Mutex
	path           string
	state          State
	journal        bool
	journalValid   int64
	journalRecords int
	writeError     error
}

func NewStore(dir, receive string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := secureDir(dir); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dir, "state.json")}
	data, err := os.ReadFile(s.path)
	if err == nil {
		if err = json.Unmarshal(data, &s.state); err != nil {
			return nil, fmt.Errorf("cannot decode durable state: %w", err)
		}
		if s.state.Schema != 1 {
			return nil, errors.New("unsupported state schema")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		for _, path := range []string{s.journalPath(), filepath.Join(dir, "jobs.json"), filepath.Join(dir, "logs", "backend.jsonl")} {
			if info, e := os.Stat(path); e == nil && (filepath.Base(path) != "backend.jsonl" || info.Size() > 0) {
				return nil, errors.New("identity snapshot is missing from an existing profile; restore a secure backup instead of generating a new identity")
			}
		}
		name, _ := os.Hostname()
		s.state = State{Schema: 1, Identity: Identity{Key: key.NewNode(), PSK: tailcat.NewPresharedKey()}, Settings: Settings{Name: name, ReceiveDir: receive, Theme: "system"}, Peers: map[string]*Peer{}, Transfers: map[string]*Transfer{}}
		if err = s.write(s.state); err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}
	if err := s.loadJournal(); err != nil {
		return nil, err
	}
	if s.state.Identity.Key.IsZero() || s.state.Identity.PSK.IsZero() || s.state.Peers == nil || s.state.Transfers == nil {
		return nil, errors.New("invalid durable state")
	}
	for id, t := range s.state.Transfers {
		if t == nil || t.ID != id || !ValidateID(id) || len(t.Done) != len(t.Manifest.Entries) {
			return nil, errors.New("invalid saved transfer")
		}
		if err := ValidateManifest(t.Manifest.Public()); err != nil {
			return nil, errors.New("invalid saved manifest")
		}
		for i, n := range t.Done {
			if n < 0 || n > t.Manifest.Entries[i].Size || (n != t.Manifest.Entries[i].Size && n%ChunkSize != 0) {
				return nil, errors.New("invalid saved checkpoint")
			}
		}
		for top, target := range t.Final {
			if _, err := ValidatePath(top); err != nil || strings.Contains(top, "/") {
				return nil, errors.New("invalid finalization journal")
			}
			if _, err := ValidatePath(target); err != nil || strings.Contains(target, "/") {
				return nil, errors.New("invalid finalization journal")
			}
		}
	}
	return s, nil
}

// Copy slices/maps without serializing immutable strings or key material.
func clone(s State) State {
	c := s
	c.Identity.Region = s.Identity.Region.Clone()
	c.Peers = make(map[string]*Peer, len(s.Peers))
	c.Transfers = make(map[string]*Transfer, len(s.Transfers))
	c.Receipts = maps.Clone(s.Receipts)
	for id, p := range s.Peers {
		if p == nil {
			continue
		}
		x := *p
		c.Peers[id] = &x
	}
	for id, t := range s.Transfers {
		if t == nil {
			continue
		}
		x := *t
		x.Manifest.Entries = slices.Clone(t.Manifest.Entries)
		for i := range x.Manifest.Entries {
			x.Manifest.Entries[i].Chunks = slices.Clone(t.Manifest.Entries[i].Chunks)
		}
		x.Done = slices.Clone(t.Done)
		x.Final = maps.Clone(t.Final)
		x.Targets = maps.Clone(t.Targets)
		c.Transfers[id] = &x
	}
	return c
}
func (s *Store) Snapshot() State { s.mu.Lock(); defer s.mu.Unlock(); return clone(s.state) }

// Update commits durable state (or a verified journal record) before publication.
func (s *Store) Update(fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeError != nil {
		return fmt.Errorf("state storage needs recovery/restart: %w", s.writeError)
	}
	next := clone(s.state)
	if err := fn(&next); err != nil {
		return err
	}
	if err := s.commit(&next); err != nil {
		return err
	}
	s.state = next
	return nil
}
func (s *Store) write(next State) error {
	b, err := json.Marshal(next)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".state-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = replaceFile(name, s.path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(s.path))
}

// Peers and KnownPeer keep frequent authentication/presence checks independent
// of potentially large manifests and transfer history.
func (s *Store) Peers() map[string]*Peer {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]*Peer, len(s.state.Peers))
	for id, p := range s.state.Peers {
		if p != nil {
			x := *p
			out[id] = &x
		}
	}
	return out
}
func (s *Store) KnownPeer(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Peers[id] != nil
}
func (s *Store) Preferences() Settings { s.mu.Lock(); defer s.mu.Unlock(); return s.state.Settings }

func (s *Store) Health() error { s.mu.Lock(); defer s.mu.Unlock(); return s.writeError }
