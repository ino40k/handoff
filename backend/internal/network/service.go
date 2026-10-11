package network

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/tailscale/tailcat"
	"handoff/internal/core"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"tailscale.com/types/key"
	"time"
)

const Port = 47291
const Protocol = 1

type peerContext struct{}
type Presence struct {
	RouteScope string    `json:"routeScope"`
	Online     bool      `json:"online"`
	Path       string    `json:"path"`
	Latency    float64   `json:"latency"`
	Seen       time.Time `json:"seen"`
}
type connection struct {
	mu        sync.RWMutex
	ctx       context.Context
	cancel    context.CancelFunc
	closed    bool
	tc        *tailcat.Client
	http      *http.Client
	transport *http.Transport
}
type Service struct {
	closeOnce   sync.Once
	Store       *core.Store
	Receiver    *core.Receiver
	Log         *core.Logger
	mu          sync.Mutex
	server      *tailcat.Server
	http        *http.Server
	allow       tailcat.KeySet
	clients     map[string]*connection
	presence    map[string]Presence
	cache       *MapCache
	ctx         context.Context
	cancel      context.CancelFunc
	ready       bool
	startError  string
	invitations map[string]*Invitation
	joins       map[string]*Join
	rates       map[string]Rate
}
type Rate struct {
	Bytes int64     `json:"bytes"`
	Speed float64   `json:"speed"`
	At    time.Time `json:"-"`
}

func New(store *core.Store, log *core.Logger, dir string) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{Store: store, Receiver: &core.Receiver{Store: store}, Log: log, clients: map[string]*connection{}, presence: map[string]Presence{}, cache: &MapCache{Dir: dir}, ctx: ctx, cancel: cancel, invitations: map[string]*Invitation{}, joins: map[string]*Join{}, rates: map[string]Rate{}}
}
func Address(s core.State) tailcat.Addr {
	return (&tailcat.ConnInfo{ServerPublic: tailcat.NodePublic{NodePublic: s.Identity.Key.Public()}, ServerDiscoPublic: tailcat.DiscoPublicForNode(s.Identity.Key), PresharedKey: s.Identity.PSK, RegionID: s.Identity.Region.RegionID}).Addr()
}
func ValidateAddress(a tailcat.Addr) (tailcat.ConnInfo, error) {
	if len(a) > 4096 {
		return tailcat.ConnInfo{}, errors.New("address too large")
	}
	ci, e := tailcat.ParseAddr(a)
	if e != nil || ci.ServerPublic.IsZero() || ci.ServerDiscoPublic.IsZero() || ci.PresharedKey.IsZero() || ci.RegionID <= 0 || len(ci.Region) != 0 {
		return tailcat.ConnInfo{}, errors.New("invalid handoff tailcat address")
	}
	return ci, nil
}
func (s *Service) Run() {
	s.Log.Event("startup", "engine_start", "", nil)
	if err := s.Receiver.Recover(); err != nil {
		s.Log.Event("recovery", "storage_recovery_failed", "", err)
		s.mu.Lock()
		s.startError = "Some saved downloads could not be recovered; see logs"
		s.mu.Unlock()
	}
	for _, t := range s.Store.Snapshot().Transfers {
		if t.Direction == "incoming" && t.Approved && !core.Terminal(t.Status) {
			event := "resume_checkpoint_verified"
			if t.Status == "failed" {
				event = "resume_checkpoint_paused"
			}
			s.Log.Progress("recovery", event, t.ID, sum(t.Done), t.Manifest.Total)
		}
	}
	for s.ctx.Err() == nil {
		if err := s.start(); err != nil {
			s.Log.Event("connectivity", "listener_start_failed", "", err)
			s.mu.Lock()
			s.startError = "Network unavailable; retrying"
			s.mu.Unlock()
			select {
			case <-s.ctx.Done():
				return
			case <-time.After(15 * time.Second):
				continue
			}
		}
		s.mu.Lock()
		s.ready = true
		s.startError = ""
		s.mu.Unlock()
		s.Log.Event("connectivity", "listener_ready", "", nil)
		go s.presenceLoop()
		go s.queueLoop()
		return
	}
}
func (s *Service) start() error {
	st := s.Store.Snapshot()
	ctx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
	defer cancel()
	if st.Identity.Region == nil {
		ci := &tailcat.ConnInfo{RegionID: -1}
		if err := ci.Expand(ctx, tailcat.ExpandForServer, s.cache); err != nil {
			return err
		}
		if len(ci.Region) != 1 {
			return errors.New("could not select relay")
		}
		if err := s.Store.Update(func(st *core.State) error { st.Identity.Region = ci.Region[0]; return nil }); err != nil {
			return err
		}
		st = s.Store.Snapshot()
	} else {
		if dm, e := tailcat.FetchDERPMap(ctx, s.cache); e == nil {
			if region := dm.Regions[st.Identity.Region.RegionID]; region != nil {
				if e = s.Store.Update(func(st *core.State) error { st.Identity.Region = region; return nil }); e != nil {
					return e
				}
				st = s.Store.Snapshot()
			}
		}
	}
	for _, p := range st.Peers {
		s.allow.Add(p.RemoteKey)
	}
	server := &tailcat.Server{Key: st.Identity.Key, PresharedKey: st.Identity.PSK, Region: st.Identity.Region, AllowClient: s.allow.Contains, Logf: s.Log.Tailcat, DERPMapCache: s.cache}
	ln, err := server.Listen(ctx, "tcp", fmt.Sprintf(":%d", Port))
	if err != nil {
		return err
	}
	if s.ctx.Err() != nil {
		ln.Close()
		server.Close()
		return s.ctx.Err()
	}
	h := &http.Server{Handler: http.HandlerFunc(s.handle), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 10 * time.Minute, WriteTimeout: 10 * time.Minute, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192, ConnContext: func(ctx context.Context, c net.Conn) context.Context {
		k, ok := server.PeerKey(c.RemoteAddr())
		if !ok {
			return ctx
		}
		return context.WithValue(ctx, peerContext{}, k)
	}}
	s.mu.Lock()
	s.server = server
	s.http = h
	s.mu.Unlock()
	go func() {
		if err := h.Serve(ln); err != nil && err != http.ErrServerClosed {
			s.Log.Event("connectivity", "listener_failed", "", err)
		}
	}()
	return nil
}
func (s *Service) Ready() (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ready, s.startError
}
func (s *Service) Presence() map[string]Presence {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := map[string]Presence{}
	for k, v := range s.presence {
		m[k] = v
	}
	return m
}
func (s *Service) Rates() map[string]Rate {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := map[string]Rate{}
	for k, v := range s.rates {
		if time.Since(v.At) > 5*time.Second {
			v.Speed = 0
		}
		m[k] = v
	}
	return m
}
func (s *Service) rate(id string, bytes int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.rates[id]
	now := time.Now()
	if !r.At.IsZero() {
		elapsed := now.Sub(r.At).Seconds()
		if elapsed > 0 {
			r.Speed = float64(bytes-r.Bytes) / elapsed
		}
	}
	r.Bytes = bytes
	r.At = now
	s.rates[id] = r
}
func (s *Service) client(p *core.Peer) *connection {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil || s.Store.Snapshot().Peers[p.ID] == nil {
		return nil
	}
	if c := s.clients[p.ID]; c != nil {
		return c
	}
	tc := &tailcat.Client{Server: p.Address, Key: p.LocalKey, Logf: s.Log.Tailcat, DERPMapCache: s.cache}
	tr := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) { return tc.DialTCPPort(ctx, Port) }, MaxIdleConnsPerHost: 1, ResponseHeaderTimeout: 10 * time.Minute, IdleConnTimeout: 30 * time.Second}
	clientCtx, clientCancel := context.WithCancel(s.ctx)
	c := &connection{ctx: clientCtx, cancel: clientCancel, tc: tc, transport: tr, http: &http.Client{Transport: tr, Timeout: 10 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	s.clients[p.ID] = c
	return c
}
func (s *Service) request(ctx context.Context, p *core.Peer, method, route string, data []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, "http://handoff"+route, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := s.client(p)
	if client == nil {
		return errors.New("peer revoked")
	}
	client.mu.RLock()
	defer client.mu.RUnlock()
	if client.closed {
		return errors.New("peer connection closed")
	}
	requestCtx, requestCancel := context.WithCancel(ctx)
	defer requestCancel()
	stop := context.AfterFunc(client.ctx, requestCancel)
	defer stop()
	req = req.WithContext(requestCtx)
	res, err := client.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		return fmt.Errorf("peer response %d: %s", res.StatusCode, core.Redact(string(b)))
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(res.Body, core.MaxManifest)).Decode(out)
	}
	return nil
}
func (s *Service) authenticated(r *http.Request) string {
	k, ok := r.Context().Value(peerContext{}).(key.NodePublic)
	if !ok {
		return ""
	}
	for id, p := range s.Store.Snapshot().Peers {
		if p.RemoteKey == k {
			return id
		}
	}
	return ""
}
func jsonReply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
func decode(w http.ResponseWriter, r *http.Request, v any, n int64) error {
	return json.NewDecoder(http.MaxBytesReader(w, r.Body, n)).Decode(v)
}
func (s *Service) handle(w http.ResponseWriter, r *http.Request) {
	peer := s.authenticated(r)
	if peer == "" {
		s.Log.Event("authentication", "request_rejected", "", nil)
		http.Error(w, "not authorized", 403)
		return
	}
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	var err error
	switch r.URL.Path {
	case "/hello":
		var hello struct {
			Version int `json:"version"`
		}
		if err = decode(w, r, &hello, 1024); err == nil && hello.Version != Protocol {
			err = errors.New("unsupported protocol")
		}
		if err == nil {
			jsonReply(w, map[string]any{"version": Protocol, "name": s.Store.Snapshot().Settings.Name})
			return
		}
	case "/offer":
		var m core.Manifest
		err = decode(w, r, &m, core.MaxManifest)
		if err == nil {
			err = s.Receiver.Offer(peer, m)
			s.Log.Event("request", "incoming_offer", m.ID, err)
		}
		if err == nil {
			jsonReply(w, s.transferStatus(peer, m.ID, m.Digest))
			return
		}
	case "/status":
		var q struct {
			ID     string `json:"id"`
			Digest string `json:"digest"`
		}
		err = decode(w, r, &q, 2048)
		if err == nil {
			status := s.transferStatus(peer, q.ID, q.Digest)
			if status == nil {
				err = errors.New("unknown transfer")
			} else {
				jsonReply(w, status)
				return
			}
		}
	case "/grant":
		var q struct {
			ID     string `json:"id"`
			Digest string `json:"digest"`
		}
		err = decode(w, r, &q, 2048)
		if err == nil {
			if status := s.transferStatus(peer, q.ID, q.Digest); status != nil && core.Terminal(status.Status) {
				jsonReply(w, map[string]any{"granted": false, "transfer": status})
				return
			}
			var granted bool
			granted, err = s.Receiver.Grant(peer, q.ID, q.Digest)
			if err == nil {
				jsonReply(w, map[string]any{"granted": granted, "transfer": s.transferStatus(peer, q.ID, q.Digest)})
				return
			}
		}
	case "/chunk":
		id := r.Header.Get("Handoff-ID")
		digest := r.Header.Get("Handoff-Digest")
		index, ie := strconv.Atoi(r.Header.Get("Handoff-Index"))
		offset, oe := strconv.ParseInt(r.Header.Get("Handoff-Offset"), 10, 64)
		if ie != nil || oe != nil {
			err = errors.New("invalid chunk coordinates")
			break
		}
		granted, gateErr := s.Receiver.Grant(peer, id, digest)
		if gateErr != nil || !granted {
			err = errors.New("receiver queue unavailable")
			break
		}
		var b []byte
		b, err = io.ReadAll(http.MaxBytesReader(w, r.Body, core.ChunkSize))
		if err == nil {
			err = s.Receiver.Put(peer, id, digest, index, offset, b)
		}
		if err == nil {
			t := s.Store.Snapshot().Transfers[id]
			s.rate(id, sum(t.Done))
			jsonReply(w, s.transferStatus(peer, id, digest))
			return
		}
		s.Log.Event("transfer", "incoming_chunk_failed", id, err)
		if isStorageError(err) {
			s.receiveFailure(peer, id, err)
		}
	case "/finish":
		var q struct {
			ID     string `json:"id"`
			Digest string `json:"digest"`
		}
		err = decode(w, r, &q, 2048)
		if err == nil {
			err = s.Receiver.Finish(peer, q.ID, q.Digest)
			s.Log.Event("transfer", "incoming_finalize", q.ID, err)
			if err != nil {
				s.receiveFailure(peer, q.ID, err)
			}
		}
		if err == nil {
			jsonReply(w, s.transferStatus(peer, q.ID, q.Digest))
			return
		}
	case "/cancel":
		var q struct {
			ID     string `json:"id"`
			Digest string `json:"digest"`
		}
		err = decode(w, r, &q, 2048)
		if err == nil {
			t := s.Store.Snapshot().Transfers[q.ID]
			if t == nil {
				if receipt, ok := s.Store.Snapshot().Receipts[q.ID]; !ok || receipt.PeerID != peer || receipt.Digest != q.Digest {
					err = errors.New("unknown transfer")
				}
			} else if t.PeerID != peer || t.Direction != "incoming" || t.Manifest.Digest != q.Digest {
				err = errors.New("unknown transfer")
			} else if !core.Terminal(t.Status) {
				err = s.Receiver.Change(q.ID, "cancel")
				s.Log.Event("queue", "remote_cancel", q.ID, err)
			}
		}
		if err == nil {
			jsonReply(w, map[string]bool{"ok": true})
			return
		}
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, core.Redact(err.Error()), 409)
	} else {
		http.Error(w, "invalid request", 400)
	}
}

type TransferStatus struct {
	Status   string  `json:"status"`
	Done     []int64 `json:"done"`
	Approved bool    `json:"approved"`
	Paused   bool    `json:"paused"`
	Error    string  `json:"error,omitempty"`
}

func (s *Service) transferStatus(peer, id, digest string) *TransferStatus {
	st := s.Store.Snapshot()
	t := st.Transfers[id]
	if t == nil {
		if receipt, ok := st.Receipts[id]; ok && receipt.PeerID == peer && receipt.Digest == digest {
			return &TransferStatus{Status: receipt.Status, Approved: receipt.Approved}
		}
	}
	if t == nil || t.PeerID != peer || t.Direction != "incoming" || t.Manifest.Digest != digest {
		return nil
	}
	return &TransferStatus{t.Status, t.Done, t.Approved, t.Paused, t.Error}
}
func sum(v []int64) int64 {
	var n int64
	for _, b := range v {
		n += b
	}
	return n
}
func (s *Service) presenceLoop() {
	for s.ctx.Err() == nil {
		peers := s.Store.Snapshot().Peers
		var wg sync.WaitGroup
		for id, p := range peers {
			wg.Add(1)
			go func(id string, p *core.Peer) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(s.ctx, 6*time.Second)
				defer cancel()
				var hello map[string]any
				err := s.request(ctx, p, "POST", "/hello", []byte(`{"version":1}`), &hello)
				pr := Presence{Online: err == nil, Path: "offline"}
				if err == nil {
					pr.Seen = time.Now().UTC()
					client := s.client(p)
					if client == nil {
						return
					}
					client.mu.RLock()
					if client.closed {
						client.mu.RUnlock()
						return
					}
					if ping, e := client.tc.DiscoPing(ctx); e == nil {
						pr.Latency = ping.LatencySeconds * 1000
						if ping.Endpoint != "" {
							pr.Path = "direct"
							if ap, e := netip.ParseAddrPort(ping.Endpoint); e == nil {
								ip := ap.Addr()
								pr.RouteScope = "internet"
								if ip.IsPrivate() || ip.IsLinkLocalUnicast() {
									pr.RouteScope = "local network"
								}
								if ip.IsLoopback() {
									pr.RouteScope = "loopback"
								}
							}
						} else if ping.DERPRegionID != 0 {
							pr.Path = "relayed"
							pr.RouteScope = "public relay"
						} else {
							pr.Path = "negotiating"
						}
					} else {
						pr.Path = "negotiating"
					}
					client.mu.RUnlock()
				}
				s.mu.Lock()
				old := s.presence[id]
				s.presence[id] = pr
				s.mu.Unlock()
				if err != nil {
					s.resetClient(id)
				}
				if old.Online != pr.Online || old.Path != pr.Path {
					s.Log.Event("connectivity", "peer_"+pr.Path, id, err)
				}
			}(id, p)
		}
		wg.Wait()
		select {
		case <-s.ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}
func (s *Service) RemovePeer(id string) error {
	st := s.Store.Snapshot()
	p := st.Peers[id]
	if p == nil {
		return errors.New("unknown peer")
	}
	// Commit revocation, then remove admission and all existing routing/streams.
	if err := s.Receiver.RevokePeer(id); err != nil {
		return err
	}

	s.allow.Remove(p.RemoteKey)
	s.mu.Lock()
	server := s.server
	c := s.clients[id]
	delete(s.clients, id)
	delete(s.presence, id)
	s.mu.Unlock()
	if server != nil {
		server.DisconnectClient(p.RemoteKey)
	}
	if c != nil {
		c.Close()
	}
	s.Log.Event("authentication", "peer_revoked", id, nil)
	return s.Receiver.CleanupCanceled()
}
func (s *Service) Close() {
	s.closeOnce.Do(func() {
		s.cancel()
		s.mu.Lock()
		h := s.http
		tc := s.server
		clients := []*connection{}
		for _, c := range s.clients {
			clients = append(clients, c)
		}
		invs := []*Invitation{}
		for _, i := range s.invitations {
			invs = append(invs, i)
		}
		joins := []*Join{}
		for _, j := range s.joins {
			joins = append(joins, j)
		}
		s.ready = false
		s.mu.Unlock()
		if h != nil {
			h.Close()
		}
		for _, i := range invs {
			i.Close()
		}
		for _, j := range joins {
			j.cancel()
		}
		for _, c := range clients {
			c.Close()
		}
		if tc != nil {
			tc.Close()
		}
		s.Log.Event("shutdown", "engine_stop", "", nil)
	})
}

func (s *Service) LocalAction(id, action, dest string) error {
	var err error
	if action == "accept" {
		err = s.Receiver.Accept(id, dest)
	} else {
		err = s.Receiver.Change(id, action)
	}
	s.Log.Event("queue", action, id, err)
	if err == nil && (action == "decline" || action == "cancel") {
		err = s.Receiver.CleanupCanceled()
	}
	return err
}
func (s *Service) Send(ctx context.Context, paths, peers []string) error {
	if len(peers) == 0 || len(paths) == 0 {
		return errors.New("select files and at least one peer")
	}
	s.Log.Event("request", "manifest_scan_started", "", nil)
	m, err := core.BuildManifest(ctx, paths)
	if err != nil {
		s.Log.Event("filesystem", "manifest_scan_failed", "", err)
		return err
	}
	return s.Store.Update(func(st *core.State) error {
		now := time.Now().UTC()
		for i, id := range peers {
			if st.Peers[id] == nil {
				return errors.New("unknown peer")
			}
			copy := m
			copy.ID = core.RandomID()
			copy.Digest = copy.CalculateDigest()
			st.Transfers[copy.ID] = &core.Transfer{ID: copy.ID, PeerID: id, Direction: "outgoing", Manifest: copy, Status: "pending", Done: make([]int64, len(copy.Entries)), Created: now, Updated: now, Order: now.UnixNano() + int64(i)}
			s.Log.Event("request", "outgoing_created", copy.ID, nil)
		}
		return nil
	})
}
func (s *Service) Reorder(id string, delta int) error {
	return s.Store.Update(func(st *core.State) error {
		t := st.Transfers[id]
		if t == nil || t.Direction != "outgoing" || core.Terminal(t.Status) {
			return errors.New("not queued")
		}
		var nearest *core.Transfer
		for _, other := range st.Transfers {
			if other.ID == id || other.Direction != "outgoing" || core.Terminal(other.Status) {
				continue
			}
			if delta < 0 && other.Order < t.Order && (nearest == nil || other.Order > nearest.Order) {
				nearest = other
			}
			if delta > 0 && other.Order > t.Order && (nearest == nil || other.Order < nearest.Order) {
				nearest = other
			}
		}
		if nearest != nil {
			t.Order, nearest.Order = nearest.Order, t.Order
		}
		return nil
	})
}
func (s *Service) ClearHistory() error {
	return s.Store.Update(func(st *core.State) error {
		for id, t := range st.Transfers {
			if core.Terminal(t.Status) && !(t.Direction == "outgoing" && t.Status == "canceled" && !t.RemoteAck) {
				if t.Direction == "incoming" {
					if st.Receipts == nil {
						st.Receipts = map[string]core.Receipt{}
					}
					st.Receipts[id] = core.Receipt{PeerID: t.PeerID, Digest: t.Manifest.Digest, Status: t.Status, Approved: t.Approved}
				}
				delete(st.Transfers, id)
			}
		}
		return nil
	})
}

func (s *Service) resetClient(id string) {
	s.mu.Lock()
	c := s.clients[id]
	delete(s.clients, id)
	s.mu.Unlock()
	if c != nil {
		c.Close()
	}
	s.Log.Event("connectivity", "reconnect_scheduled", id, nil)
}

func (c *connection) Close() {
	c.cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	c.transport.CloseIdleConnections()
	c.tc.Close()
}

func isStorageError(err error) bool {
	var pe *os.PathError
	return errors.As(err, &pe) || strings.Contains(err.Error(), "disk space") || strings.Contains(err.Error(), "integrity failure")
}
func (s *Service) receiveFailure(peer, id string, err error) {
	s.Store.Update(func(st *core.State) error {
		t := st.Transfers[id]
		if t != nil && t.PeerID == peer && t.Direction == "incoming" && t.Approved && !core.Terminal(t.Status) {
			t.Status = "failed"
			t.Paused = true
			t.Error = core.Redact(err.Error())
		}
		return nil
	})
}
