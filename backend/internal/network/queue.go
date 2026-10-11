package network

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"handoff/internal/core"
	"net/http"
	"sort"
	"time"
)

func (s *Service) queueLoop() {
	go s.controlLoop()
	for s.ctx.Err() == nil {
		st := s.Store.Snapshot()
		var queue []*core.Transfer
		for _, t := range st.Transfers {
			if t.Direction == "outgoing" && t.Approved && !t.Paused && (t.Status == "queued" || t.Status == "active") {
				queue = append(queue, t)
			}
		}
		sort.Slice(queue, func(i, j int) bool {
			if queue[i].Status == "active" && queue[j].Status != "active" {
				return true
			}
			if queue[j].Status == "active" && queue[i].Status != "active" {
				return false
			}
			return queue[i].Order < queue[j].Order
		})
		worked := false
		for _, t := range queue {
			p := st.Peers[t.PeerID]
			if p == nil {
				continue
			}
			if pr := s.Presence()[p.ID]; !pr.Online {
				continue
			}
			worked = true
			err := s.sendNext(p, t)
			if err != nil {
				s.Log.Event("transfer", "send_interrupted", t.ID, err)
				s.Store.Update(func(st *core.State) error {
					x := st.Transfers[t.ID]
					if x != nil && !core.Terminal(x.Status) && !x.Paused {
						x.Status = "queued"
						x.Error = "Connection interrupted; will retry"
					}
					return nil
				})
				select {
				case <-s.ctx.Done():
					return
				case <-time.After(3 * time.Second):
				}
			}
			break
		}
		delay := 100 * time.Millisecond
		if !worked {
			delay = time.Second
		}
		select {
		case <-s.ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}
func (s *Service) controlLoop() {
	for s.ctx.Err() == nil {
		st := s.Store.Snapshot()
		for _, t := range st.Transfers {
			if t.Direction != "outgoing" || (core.Terminal(t.Status) && t.Status != "canceled") || t.Status == "failed" || t.Status == "active" {
				continue
			}
			p := st.Peers[t.PeerID]
			if p == nil {
				continue
			}
			ctx, cancel := context.WithTimeout(s.ctx, 8*time.Second)
			s.syncTransfer(ctx, p, t)
			cancel()
		}
		select {
		case <-s.ctx.Done():
			return
		case <-time.After(4 * time.Second):
		}
	}
}
func (s *Service) syncTransfer(ctx context.Context, p *core.Peer, t *core.Transfer) {
	q, _ := json.Marshal(map[string]string{"id": t.ID, "digest": t.Manifest.Digest})
	if t.Status == "canceled" {
		if t.RemoteAck {
			return
		}
		if err := s.request(ctx, p, "POST", "/cancel", q, nil); err == nil {
			s.Store.Update(func(st *core.State) error {
				if x := st.Transfers[t.ID]; x != nil {
					x.RemoteAck = true
				}
				return nil
			})
		}
		return
	}
	var status TransferStatus
	var err error
	if !t.Offered {
		data, _ := json.Marshal(t.Manifest.Public())
		err = s.request(ctx, p, "POST", "/offer", data, &status)
	} else {
		err = s.request(ctx, p, "POST", "/status", q, &status)
	}
	if err != nil {
		return
	}
	s.applyStatus(t, status)
}
func (s *Service) applyStatus(t *core.Transfer, status TransferStatus) error {
	if len(status.Done) == 0 && core.Terminal(status.Status) {
		status.Done = append([]int64(nil), t.Done...)
		if status.Status == "completed" {
			for i, e := range t.Manifest.Entries {
				status.Done[i] = e.Size
			}
		}
	}
	if len(status.Done) != len(t.Done) {
		return errors.New("invalid peer progress")
	}
	for i, n := range status.Done {
		e := t.Manifest.Entries[i]
		if n < 0 || n > e.Size || (n != e.Size && n%core.ChunkSize != 0) {
			return errors.New("invalid peer offset")
		}
	}
	return s.Store.Update(func(st *core.State) error {
		x := st.Transfers[t.ID]
		if x == nil || core.Terminal(x.Status) {
			return nil
		}
		old := x.Status
		x.Offered = true
		x.Approved = status.Approved
		x.Done = status.Done
		if core.Terminal(status.Status) {
			x.Status = status.Status
			x.RemoteAck = true
			x.Error = ""
		} else if !x.Paused {
			if status.Paused {
				x.Status = "remote_paused"
				x.Error = status.Error
			} else if status.Approved {
				x.Status = "queued"
				if status.Status == "active" {
					x.Status = "active"
				}
				x.Error = ""
			} else {
				x.Status = "pending"
			}
		}
		x.Updated = time.Now().UTC()
		if old != x.Status {
			s.Log.Event("queue", "remote_"+x.Status, t.ID, nil)
		}
		return nil
	})
}
func (s *Service) sendNext(p *core.Peer, t *core.Transfer) error {
	ctx, cancel := context.WithTimeout(s.ctx, 90*time.Second)
	defer cancel()
	q, _ := json.Marshal(map[string]string{"id": t.ID, "digest": t.Manifest.Digest})
	var grant struct {
		Granted  bool           `json:"granted"`
		Transfer TransferStatus `json:"transfer"`
	}
	if err := s.request(ctx, p, "POST", "/grant", q, &grant); err != nil {
		return err
	}
	status := grant.Transfer
	if !grant.Granted {
		if err := s.applyStatus(t, status); err != nil {
			return err
		}
		select {
		case <-s.ctx.Done():
		case <-time.After(time.Second):
		}
		return nil
	}
	if err := s.applyStatus(t, status); err != nil {
		return err
	}
	fresh := s.Store.Snapshot().Transfers[t.ID]
	if fresh == nil || fresh.Paused || core.Terminal(fresh.Status) || status.Paused || !status.Approved {
		return nil
	}
	for i, e := range t.Manifest.Entries {
		if e.Dir || status.Done[i] == e.Size {
			continue
		}
		offset := status.Done[i]
		if _, ok := s.Rates()[t.ID]; !ok {
			s.Log.Progress("transfer", "stream_started", t.ID, sum(status.Done), t.Manifest.Total)
		}
		data, err := core.ReadChunk(e, offset)
		if err != nil {
			s.Store.Update(func(st *core.State) error {
				if x := st.Transfers[t.ID]; x != nil && !core.Terminal(x.Status) {
					x.Status = "failed"
					x.Error = err.Error()
				}
				return nil
			})
			s.Log.Event("filesystem", "source_changed", t.ID, err)
			return nil
		}
		if err = s.Store.Update(func(st *core.State) error {
			x := st.Transfers[t.ID]
			if x == nil || x.Paused || core.Terminal(x.Status) || st.Peers[p.ID] == nil {
				return errors.New("stopped")
			}
			x.Status = "active"
			return nil
		}); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, "POST", "http://handoff/chunk", bytes.NewReader(data))
		if err != nil {
			return err
		}
		req.Header.Set("Handoff-ID", t.ID)
		req.Header.Set("Handoff-Digest", t.Manifest.Digest)
		req.Header.Set("Handoff-Index", fmt.Sprint(i))
		req.Header.Set("Handoff-Offset", fmt.Sprint(offset))
		client := s.client(p)
		if client == nil {
			return errors.New("peer revoked")
		}
		client.mu.RLock()
		defer client.mu.RUnlock()
		if client.closed {
			return errors.New("peer connection closed")
		}
		reqCtx, reqCancel := context.WithCancel(ctx)
		defer reqCancel()
		stop := context.AfterFunc(client.ctx, reqCancel)
		defer stop()
		req = req.WithContext(reqCtx)
		res, err := client.http.Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			return fmt.Errorf("receiver temporarily unavailable (%d)", res.StatusCode)
		}
		var next TransferStatus
		if err = json.NewDecoder(res.Body).Decode(&next); err != nil {
			return err
		}
		if err = s.applyStatus(t, next); err != nil {
			return err
		}
		s.rate(t.ID, sum(next.Done))
		return nil
	}
	if err := s.request(ctx, p, "POST", "/finish", q, &status); err != nil {
		return err
	}
	err := s.applyStatus(t, status)
	s.Log.Event("transfer", "outgoing_complete", t.ID, err)
	return err
}
