package network

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/tailscale/tailcat"
	"handoff/internal/core"
	"net"
	"net/http"
	"sync"
	"tailscale.com/types/key"
	"time"
	"unicode/utf8"
)

type inviteCode struct {
	V       int          `json:"v"`
	Name    string       `json:"name"`
	Address tailcat.Addr `json:"address"`
	Token   string       `json:"token"`
	Expires time.Time    `json:"expires"`
}
type PairInfo struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Fingerprint string    `json:"fingerprint"`
	Status      string    `json:"status"`
	Expires     time.Time `json:"expires"`
	Incoming    bool      `json:"incoming"`
}
type pairRequest struct {
	Token   string       `json:"token"`
	Name    string       `json:"name"`
	Address tailcat.Addr `json:"address"`
}
type pairReply struct {
	Status    string         `json:"status"`
	Name      string         `json:"name,omitempty"`
	Address   tailcat.Addr   `json:"address,omitempty"`
	ClientKey key.NodePublic `json:"clientKey"`
}
type Invitation struct {
	mu        sync.Mutex
	id, token string
	expires   time.Time
	server    *tailcat.Server
	http      *http.Server
	status    string
	candidate *core.Peer
	name      string
	closeOnce sync.Once
}

func (i *Invitation) Close() { i.closeOnce.Do(func() { i.http.Close(); i.server.Close() }) }

type Join struct {
	mu     sync.Mutex
	ID     string
	info   PairInfo
	client *tailcat.Client
	cancel context.CancelFunc
}

func ValidName(n string) bool {
	return utf8.ValidString(n) && len(n) > 0 && utf8.RuneCountInString(n) <= 80 && len(n) <= 320 && !(bytes.ContainsAny([]byte(n), "\r\n\x00"))
}
func (s *Service) CreateInvitation() (string, error) {
	ready, _ := s.Ready()
	if !ready {
		return "", errors.New("network listener is not ready")
	}
	s.mu.Lock()
	for _, old := range s.invitations {
		old.Close()
	}
	s.invitations = map[string]*Invitation{}
	s.mu.Unlock()
	st := s.Store.Snapshot()
	server := &tailcat.Server{Key: key.NewNode(), PresharedKey: tailcat.NewPresharedKey(), Region: st.Identity.Region, Logf: s.Log.Tailcat}
	ctx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
	defer cancel()
	ln, err := server.Listen(ctx, "tcp", fmt.Sprintf(":%d", Port))
	if err != nil {
		return "", err
	}
	id := core.RandomID()
	inv := &Invitation{id: id, token: core.RandomID() + core.RandomID(), expires: time.Now().UTC().Add(15 * time.Minute), server: server, status: "waiting"}
	inv.http = &http.Server{ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, MaxHeaderBytes: 4096, ConnContext: func(ctx context.Context, c net.Conn) context.Context {
		k, ok := server.PeerKey(c.RemoteAddr())
		if !ok {
			return ctx
		}
		return context.WithValue(ctx, peerContext{}, k)
	}, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.handlePair(inv, w, r) })}
	ci := &tailcat.ConnInfo{ServerPublic: tailcat.NodePublic{NodePublic: server.Key.Public()}, ServerDiscoPublic: tailcat.DiscoPublicForNode(server.Key), PresharedKey: server.PresharedKey, RegionID: st.Identity.Region.RegionID}
	payload, _ := json.Marshal(inviteCode{Protocol, st.Settings.Name, ci.Addr(), inv.token, inv.expires})
	code := "handoff1." + base64.RawURLEncoding.EncodeToString(payload)
	s.mu.Lock()
	s.invitations[id] = inv
	s.mu.Unlock()
	go inv.http.Serve(ln)
	go func() {
		select {
		case <-s.ctx.Done():
		case <-time.After(time.Until(inv.expires)):
		}
		inv.mu.Lock()
		if inv.status != "paired" {
			inv.status = "expired"
		}
		inv.mu.Unlock()
		inv.Close()
	}()
	s.Log.Event("pairing", "invitation_created", id, nil)
	return code, nil
}
func ParseInvitation(code string) (inviteCode, error) {
	var c inviteCode
	if len(code) > 8192 || len(code) < 10 || code[:9] != "handoff1." {
		return c, errors.New("invalid invitation")
	}
	b, err := base64.RawURLEncoding.DecodeString(code[9:])
	if err != nil {
		return c, errors.New("invalid invitation")
	}
	if err = json.Unmarshal(b, &c); err != nil {
		return c, errors.New("invalid invitation")
	}
	if c.V != Protocol || len(c.Token) != 64 || !ValidName(c.Name) || time.Now().After(c.Expires) || time.Until(c.Expires) > 16*time.Minute {
		return c, errors.New("invitation expired or invalid")
	}
	if _, err = ValidateAddress(c.Address); err != nil {
		return c, err
	}
	return c, nil
}
func (s *Service) handlePair(inv *Invitation, w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	k, ok := r.Context().Value(peerContext{}).(key.NodePublic)
	if !ok {
		http.Error(w, "not authenticated", 403)
		return
	}
	var req pairRequest
	if decode(w, r, &req, 8192) != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	inv.mu.Lock()
	defer inv.mu.Unlock()
	if time.Now().After(inv.expires) || subtle.ConstantTimeCompare([]byte(req.Token), []byte(inv.token)) != 1 {
		http.Error(w, "invitation expired or invalid", 403)
		return
	}
	if inv.candidate != nil && inv.candidate.RemoteKey != k {
		http.Error(w, "invitation already claimed", 409)
		return
	}
	switch r.URL.Path {
	case "/join":
		if inv.status == "declined" || inv.status == "paired" {
			http.Error(w, "invitation used", 403)
			return
		}
		if inv.candidate == nil {
			ci, err := ValidateAddress(req.Address)
			if err != nil || !ValidName(req.Name) {
				http.Error(w, "invalid peer", 400)
				return
			}
			st := s.Store.Snapshot()
			if ci.ServerPublic.NodePublic == st.Identity.Key.Public() {
				http.Error(w, "cannot pair with yourself", 400)
				return
			}
			id := core.PeerID(ci.ServerPublic.NodePublic)
			if st.Peers[id] != nil {
				http.Error(w, "peer already paired; remove it on both devices before pairing again", 409)
				return
			}
			inv.candidate = &core.Peer{ID: id, Name: req.Name, Address: req.Address, ServerKey: ci.ServerPublic.NodePublic, RemoteKey: k, LocalKey: key.NewNode()}
			inv.name = req.Name
			inv.status = "approval"
			s.Log.Event("pairing", "approval_requested", id, nil)
		}
	case "/ack":
		if inv.status != "approved" {
			http.Error(w, "not approved", 409)
			return
		}
		inv.status = "paired"
		s.Log.Event("pairing", "pairing_completed", inv.candidate.ID, nil)
		go func() { time.Sleep(time.Second); inv.Close() }()
	default:
		http.NotFound(w, r)
		return
	}
	reply := pairReply{Status: inv.status}
	if inv.status == "approved" || inv.status == "paired" {
		st := s.Store.Snapshot()
		reply.Name = st.Settings.Name
		reply.Address = Address(st)
		reply.ClientKey = inv.candidate.LocalKey.Public()
	}
	jsonReply(w, reply)
}
func (s *Service) PairDecision(id string, approve bool) error {
	s.mu.Lock()
	inv := s.invitations[id]
	s.mu.Unlock()
	if inv == nil {
		return errors.New("unknown invitation")
	}
	inv.mu.Lock()
	defer inv.mu.Unlock()
	if inv.status != "approval" || time.Now().After(inv.expires) {
		return errors.New("pairing is not awaiting approval")
	}
	if !approve {
		inv.status = "declined"
		s.Log.Event("pairing", "pairing_declined", id, nil)
		return nil
	}
	p := inv.candidate
	if err := s.Store.Update(func(st *core.State) error {
		if st.Peers[p.ID] != nil {
			return errors.New("already paired")
		}
		st.Peers[p.ID] = p
		return nil
	}); err != nil {
		return err
	}
	s.allow.Add(p.RemoteKey)
	inv.status = "approved"
	s.Log.Event("pairing", "pairing_approved", p.ID, nil)
	return nil
}
func (s *Service) JoinInvitation(code string) error {
	ready, _ := s.Ready()
	if !ready {
		return errors.New("network is not ready")
	}
	c, err := ParseInvitation(code)
	if err != nil {
		return err
	}
	s.mu.Lock()
	for _, j := range s.joins {
		j.mu.Lock()
		ongoing := j.info.Status == "connecting" || j.info.Status == "waiting"
		j.mu.Unlock()
		if ongoing {
			s.mu.Unlock()
			return errors.New("a pairing request is already in progress")
		}
	}
	s.mu.Unlock()
	st := s.Store.Snapshot()
	local := key.NewNode()
	client := &tailcat.Client{Server: c.Address, Key: local, Logf: s.Log.Tailcat, DERPMapCache: s.cache}
	ctx, cancel := context.WithDeadline(s.ctx, c.Expires)
	id := core.RandomID()
	ci, _ := ValidateAddress(c.Address)
	j := &Join{ID: id, client: client, cancel: cancel, info: PairInfo{ID: id, Name: c.Name, Fingerprint: core.PeerID(ci.ServerPublic.NodePublic), Status: "connecting", Expires: c.Expires}}
	s.mu.Lock()
	s.joins[id] = j
	s.mu.Unlock()
	s.background(func() {
		defer cancel()
		defer client.Close()
		tr := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return client.DialTCPPort(ctx, Port)
		}}
		defer tr.CloseIdleConnections()
		hc := &http.Client{Transport: tr, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		reqData, _ := json.Marshal(pairRequest{c.Token, st.Settings.Name, Address(st)})
		setStatus := func(status string) { j.mu.Lock(); j.info.Status = status; j.mu.Unlock() }
		for ctx.Err() == nil {
			req, _ := http.NewRequestWithContext(ctx, "POST", "http://handoff/join", bytes.NewReader(reqData))
			res, e := hc.Do(req)
			if e == nil {
				var reply pairReply
				if res.StatusCode == 200 {
					e = json.NewDecoder(http.MaxBytesReader(nil, res.Body, 8192)).Decode(&reply)
				} else {
					e = errors.New("invitation rejected")
				}
				res.Body.Close()
				if e == nil && reply.Status == "approved" {
					ci, e := ValidateAddress(reply.Address)
					if e != nil || reply.ClientKey.IsZero() || !ValidName(reply.Name) {
						setStatus("failed")
						return
					}
					pid := core.PeerID(ci.ServerPublic.NodePublic)
					if ci.ServerPublic.NodePublic == st.Identity.Key.Public() {
						setStatus("failed")
						return
					}
					p := &core.Peer{ID: pid, Name: reply.Name, Address: reply.Address, ServerKey: ci.ServerPublic.NodePublic, RemoteKey: reply.ClientKey, LocalKey: local}
					// Joining explicitly approves the invitation owner. The inviter's
					// approval is now durable; persist the full authenticated reciprocal link.
					e = s.Store.Update(func(st *core.State) error {
						if st.Peers[pid] != nil {
							return errors.New("peer already paired")
						}
						st.Peers[pid] = p
						return nil
					})
					if e != nil {
						setStatus("failed")
						s.Log.Event("pairing", "pair_persistence_failed", id, e)
						return
					}
					s.allow.Add(p.RemoteKey)
					ack, _ := http.NewRequestWithContext(ctx, "POST", "http://handoff/ack", bytes.NewReader(reqData))
					if response, e := hc.Do(ack); e == nil {
						response.Body.Close()
					}
					setStatus("paired")
					s.Log.Event("pairing", "pairing_completed", pid, nil)
					return
				}
				if e == nil && reply.Status == "declined" {
					setStatus("declined")
					return
				}
				setStatus("waiting")
			}
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
			}
		}
		setStatus("expired")
		s.Log.Event("pairing", "pairing_expired", id, nil)
	})
	s.Log.Event("pairing", "invitation_join_started", id, nil)
	return nil
}
func (s *Service) Pairings() []PairInfo {
	s.mu.Lock()
	invs := make([]*Invitation, 0, len(s.invitations))
	for _, i := range s.invitations {
		invs = append(invs, i)
	}
	joins := make([]*Join, 0, len(s.joins))
	for _, j := range s.joins {
		joins = append(joins, j)
	}
	s.mu.Unlock()
	out := []PairInfo{}
	for _, i := range invs {
		i.mu.Lock()
		fingerprint := ""
		if i.candidate != nil {
			fingerprint = i.candidate.ID
		}
		out = append(out, PairInfo{i.id, i.name, fingerprint, i.status, i.expires, true})
		i.mu.Unlock()
	}
	for _, j := range joins {
		j.mu.Lock()
		out = append(out, j.info)
		j.mu.Unlock()
	}
	return out
}
