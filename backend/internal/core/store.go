package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/tailscale/tailcat"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"tailscale.com/types/key"
)

type Store struct {
	mu    sync.Mutex
	path  string
	state State
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
		name, _ := os.Hostname()
		s.state = State{Schema: 1, Identity: Identity{Key: key.NewNode(), PSK: tailcat.NewPresharedKey()}, Settings: Settings{Name: name, ReceiveDir: receive, Theme: "system"}, Peers: map[string]*Peer{}, Transfers: map[string]*Transfer{}}
		if err = s.write(s.state); err != nil {
			return nil, err
		}
	} else {
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
func clone(s State) State        { b, _ := json.Marshal(s); var c State; json.Unmarshal(b, &c); return c }
func (s *Store) Snapshot() State { s.mu.Lock(); defer s.mu.Unlock(); return clone(s.state) }

// Update commits the complete state before publishing it to any worker.
func (s *Store) Update(fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := clone(s.state)
	if err := fn(&next); err != nil {
		return err
	}
	if err := s.write(next); err != nil {
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
