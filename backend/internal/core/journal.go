package core

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
)

type transferDelta struct {
	ID     string        `json:"id"`
	Fields *Transfer     `json:"fields"`
	Done   map[int]int64 `json:"done,omitempty"`
}
type journalUpdate struct {
	Revision uint64          `json:"revision"`
	Changes  []transferDelta `json:"changes"`
}
type journalRecord struct {
	Payload  json.RawMessage `json:"payload"`
	Checksum string          `json:"checksum"`
}

func (s *Store) journalPath() string { return filepath.Join(filepath.Dir(s.path), "progress.wal") }

// EnableJournal is for the exclusively locked headless host. Desktop continues
// full-state transactions. All new hosts can read either representation.
func (s *Store) EnableJournal() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.journalPath(), os.O_RDWR, 0600)
	if err == nil {
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return err
		}
		if info.Size() != s.journalValid {
			if err = f.Truncate(s.journalValid); err != nil {
				return err
			}
			if err = f.Sync(); err != nil {
				return err
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	s.journal = true
	return nil
}
func (s *Store) loadJournal() error {
	f, err := os.Open(s.journalPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() > 64<<20 {
		return errors.New("progress journal exceeds recovery limit")
	}
	reader := bufio.NewReader(f)
	for {
		line, err := reader.ReadBytes('\n')
		if err == io.EOF {
			return nil
		} // An unacknowledged torn tail is discarded only by the exclusive writer.
		if err != nil {
			return err
		}
		var record journalRecord
		if err = json.Unmarshal(line, &record); err != nil {
			return errors.New("corrupt progress journal")
		}
		hash := sha256.Sum256(record.Payload)
		if record.Checksum != hex.EncodeToString(hash[:]) {
			return errors.New("progress journal checksum mismatch")
		}
		var update journalUpdate
		if err = json.Unmarshal(record.Payload, &update); err != nil {
			return err
		}
		if update.Revision > s.state.Revision {
			if update.Revision != s.state.Revision+1 {
				return errors.New("progress journal revision gap")
			}
			for _, d := range update.Changes {
				old := s.state.Transfers[d.ID]
				if old == nil || d.Fields == nil || d.Fields.ID != d.ID {
					return errors.New("unknown transfer in progress journal")
				}
				next := *d.Fields
				next.Manifest = old.Manifest
				next.Targets = old.Targets
				next.Done = append([]int64(nil), old.Done...)
				for index, n := range d.Done {
					if index < 0 || index >= len(next.Done) {
						return errors.New("invalid journal checkpoint index")
					}
					next.Done[index] = n
				}
				s.state.Transfers[d.ID] = &next
			}
			s.state.Revision = update.Revision
		}
		s.journalValid += int64(len(line))
		s.journalRecords++
	}
}
func mutable(t *Transfer) Transfer {
	x := *t
	x.Manifest = Manifest{}
	x.Done = nil
	x.Targets = nil
	return x
}
func deltas(old, next State) ([]transferDelta, bool) {
	if !reflect.DeepEqual(old.Identity, next.Identity) || old.Settings != next.Settings || !reflect.DeepEqual(old.Peers, next.Peers) || !reflect.DeepEqual(old.Receipts, next.Receipts) || len(old.Transfers) != len(next.Transfers) {
		return nil, false
	}
	changes := []transferDelta{}
	for id, t := range next.Transfers {
		prior := old.Transfers[id]
		if t == nil || prior == nil || !reflect.DeepEqual(prior.Manifest, t.Manifest) || !reflect.DeepEqual(prior.Targets, t.Targets) || len(prior.Done) != len(t.Done) {
			return nil, false
		}
		fields := mutable(t)
		before := mutable(prior)
		done := map[int]int64{}
		for i, n := range t.Done {
			if n != prior.Done[i] {
				done[i] = n
			}
		}
		if len(done) == 0 && reflect.DeepEqual(before, fields) {
			continue
		}
		changes = append(changes, transferDelta{ID: id, Fields: &fields, Done: done})
	}
	return changes, true
}
func (s *Store) commit(next *State) error {
	if s.journal || s.state.Revision > 0 {
		next.Revision = s.state.Revision + 1
	}
	if s.journal && s.journalRecords < 256 && s.journalValid < 8<<20 {
		if changes, ok := deltas(s.state, *next); ok {
			if len(changes) == 0 {
				next.Revision = s.state.Revision
				return nil
			}
			payload, err := json.Marshal(journalUpdate{Revision: next.Revision, Changes: changes})
			if err != nil {
				return err
			}
			hash := sha256.Sum256(payload)
			line, err := json.Marshal(journalRecord{Payload: payload, Checksum: hex.EncodeToString(hash[:])})
			if err != nil {
				return err
			}
			line = append(line, '\n')
			f, err := os.OpenFile(s.journalPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
			if err != nil {
				return err
			}
			n, err := f.Write(line)
			if err == nil && n != len(line) {
				err = io.ErrShortWrite
			}
			if err == nil {
				err = f.Sync()
			}
			ce := f.Close()
			if err == nil {
				err = ce
			}
			if err == nil && s.journalValid == 0 {
				err = syncDir(filepath.Dir(s.path))
			}
			if err != nil {
				s.writeError = err
				return err
			} // Do not append beyond an uncertain commit.
			s.journalValid += int64(len(line))
			s.journalRecords++
			return nil
		}
	}
	if err := s.write(*next); err != nil {
		s.writeError = err
		return err
	}
	// The atomic checkpoint's revision makes an old WAL harmless if compaction is
	// interrupted here. Never truncate a journal before the checkpoint is durable.
	f, err := os.OpenFile(s.journalPath(), os.O_WRONLY|os.O_TRUNC, 0600)
	if err == nil {
		err = f.Sync()
		f.Close()
	}
	if err == nil || errors.Is(err, os.ErrNotExist) {
		s.journalValid = 0
		s.journalRecords = 0
	}
	return nil
}
