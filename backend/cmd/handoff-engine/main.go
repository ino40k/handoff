package main

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"handoff/internal/core"
	"handoff/internal/network"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

type request struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}
type app struct {
	store       *core.Store
	service     *network.Service
	log         *core.Logger
	dir, logDir string
	out         sync.Mutex
}

func (a *app) reply(id string, result any, err error) {
	a.out.Lock()
	defer a.out.Unlock()
	m := map[string]any{"id": id, "result": result}
	if err != nil {
		m["error"] = core.Redact(err.Error())
	}
	if e := json.NewEncoder(os.Stdout).Encode(m); e != nil {
		a.log.Event("ipc", "response_failed", id, e)
	}
}
func main() {
	os.Unsetenv("TS_DEBUG_ADDR")
	dir := os.Getenv("HANDOFF_DATA_DIR")
	if dir == "" {
		fmt.Fprintln(os.Stderr, "HANDOFF_DATA_DIR is required")
		os.Exit(2)
	}
	receive := os.Getenv("HANDOFF_RECEIVE_DIR")
	if receive == "" {
		receive = filepath.Join(dir, "received")
	}
	logs := filepath.Join(dir, "logs")
	log, err := core.NewLogger(logs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Cannot open diagnostic logs")
		os.Exit(1)
	}
	defer log.Close()
	defer func() {
		if recover() != nil {
			log.Event("exception", "engine_panic", "", errors.New("unexpected backend panic"))
			os.Exit(1)
		}
	}()
	store, err := core.NewStore(dir, receive)
	if err != nil {
		log.Event("storage", "state_open_failed", "", err)
		fmt.Fprintln(os.Stderr, "Cannot open durable state; see backend logs")
		os.Exit(1)
	}
	service := network.New(store, log, dir)
	a := &app{store: store, service: service, log: log, dir: dir, logDir: logs}
	go service.Run()
	defer service.Close()
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	sem := make(chan struct{}, 16)
	var wg sync.WaitGroup
	for scanner.Scan() {
		var req request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			a.reply("", nil, errors.New("invalid IPC frame"))
			continue
		}
		if req.Method == "quit" {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(req request) {
			defer wg.Done()
			defer func() {
				<-sem
				if recover() != nil {
					a.log.Event("exception", "command_panic", req.ID, errors.New("unexpected command panic"))
					a.reply(req.ID, nil, errors.New("unexpected backend error; see logs"))
				}
			}()
			if req.Method != "snapshot" {
				a.log.Event("ipc", req.Method, req.ID, nil)
			}
			result, err := a.command(req)
			a.reply(req.ID, result, err)
		}(req)
	}
	service.Close()
	wg.Wait()
	if scanner.Err() != nil {
		log.Event("ipc", "input_failed", "", scanner.Err())
	}
}
func (a *app) command(req request) (any, error) {
	var p struct {
		ID          string        `json:"id"`
		Name        string        `json:"name"`
		Code        string        `json:"code"`
		Approve     bool          `json:"approve"`
		Action      string        `json:"action"`
		Destination string        `json:"destination"`
		Paths       []string      `json:"paths"`
		Peers       []string      `json:"peers"`
		Settings    core.Settings `json:"settings"`
		Delta       int           `json:"delta"`
	}
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, errors.New("invalid command parameters")
		}
	}
	switch req.Method {
	case "snapshot":
		return a.snapshot(), nil
	case "invite":
		code, err := a.service.CreateInvitation()
		return map[string]string{"code": code}, err
	case "join":
		return nil, a.service.JoinInvitation(strings.TrimSpace(p.Code))
	case "pairDecision":
		return nil, a.service.PairDecision(p.ID, p.Approve)
	case "removePeer":
		return nil, a.service.RemovePeer(p.ID)
	case "renamePeer":
		if !network.ValidName(p.Name) {
			return nil, errors.New("name must contain 1–80 characters")
		}
		return nil, a.store.Update(func(s *core.State) error {
			peer := s.Peers[p.ID]
			if peer == nil {
				return errors.New("unknown peer")
			}
			peer.Name = p.Name
			return nil
		})
	case "send":
		return nil, a.service.Send(context.Background(), p.Paths, p.Peers)
	case "transfer":
		return nil, a.service.LocalAction(p.ID, p.Action, p.Destination)
	case "reorder":
		return nil, a.service.Reorder(p.ID, p.Delta)
	case "clearHistory":
		return nil, a.service.ClearHistory()
	case "settings":
		if !network.ValidName(p.Settings.Name) {
			return nil, errors.New("name must contain 1–80 characters")
		}
		if p.Settings.Theme != "system" && p.Settings.Theme != "dark" && p.Settings.Theme != "light" {
			return nil, errors.New("invalid theme")
		}
		if !filepath.IsAbs(p.Settings.ReceiveDir) {
			return nil, errors.New("receiving directory must be absolute")
		}
		root, err := os.OpenRoot(p.Settings.ReceiveDir)
		if err != nil {
			return nil, err
		}
		root.Close()
		return nil, a.store.Update(func(s *core.State) error { s.Settings = p.Settings; return nil })
	case "diagnostics":
		path, err := a.diagnostics()
		return map[string]string{"path": path}, err
	default:
		return nil, errors.New("unknown command")
	}
}
func (a *app) snapshot() any {
	st := a.store.Snapshot()
	pr := a.service.Presence()
	rates := a.service.Rates()
	peers := []any{}
	ids := []string{}
	for id := range st.Peers {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return strings.ToLower(st.Peers[ids[i]].Name) < strings.ToLower(st.Peers[ids[j]].Name)
	})
	for _, id := range ids {
		p := st.Peers[id]
		status := pr[id]
		peers = append(peers, map[string]any{"id": id, "name": p.Name, "online": status.Online, "connection": status.Path, "latency": status.Latency, "routeScope": status.RouteScope, "fingerprint": id})
	}
	ts := []*core.Transfer{}
	for _, t := range st.Transfers {
		ts = append(ts, t)
	}
	sort.Slice(ts, func(i, j int) bool {
		if core.Terminal(ts[i].Status) != core.Terminal(ts[j].Status) {
			return !core.Terminal(ts[i].Status)
		}
		return ts[i].Order < ts[j].Order
	})
	transfers := []any{}
	for _, t := range ts {
		entries := []any{}
		var done int64
		for i, e := range t.Manifest.Entries {
			n := int64(0)
			if i < len(t.Done) {
				n = t.Done[i]
			}
			done += n
			entries = append(entries, map[string]any{"path": e.Path, "dir": e.Dir, "size": e.Size, "done": n})
		}
		name := t.PeerID
		if p := st.Peers[t.PeerID]; p != nil {
			name = p.Name
		}
		label := ""
		if len(t.Manifest.Entries) > 0 {
			label = t.Manifest.Entries[0].Path
		}
		if len(t.Manifest.Entries) > 1 {
			label += fmt.Sprintf(" · %d items", len(t.Manifest.Entries))
		}
		speed := rates[t.ID].Speed
		eta := float64(0)
		if speed > 0 {
			eta = float64(t.Manifest.Total-done) / speed
		}
		transfers = append(transfers, map[string]any{"id": t.ID, "peerId": t.PeerID, "peer": name, "direction": t.Direction, "label": label, "status": t.Status, "approved": t.Approved, "paused": t.Paused, "size": t.Manifest.Total, "done": done, "speed": speed, "eta": eta, "error": t.Error, "destination": t.Destination, "entries": entries, "created": t.Created, "final": t.Final})
	}
	ready, err := a.service.Ready()
	return map[string]any{"version": core.Version, "deviceId": core.PeerID(st.Identity.Key.Public()), "settings": st.Settings, "peers": peers, "transfers": transfers, "pairings": a.service.Pairings(), "ready": ready, "networkError": err, "logDir": a.logDir}
}
func (a *app) diagnostics() (string, error) {
	name := filepath.Join(a.logDir, "handoff-diagnostics-"+time.Now().UTC().Format("20060102-150405")+".zip")
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	zw := zip.NewWriter(f)
	fail := func(err error) (string, error) { zw.Close(); f.Close(); os.Remove(name); return "", err }
	st := a.store.Snapshot()
	summary := map[string]any{"version": core.Version, "os": runtime.GOOS, "arch": runtime.GOARCH, "peers": len(st.Peers), "transfers": len(st.Transfers), "network": a.service.Presence()}
	out, err := zw.Create("summary.json")
	if err != nil {
		return fail(err)
	}
	json.NewEncoder(out).Encode(summary)
	files, _ := filepath.Glob(filepath.Join(a.logDir, "*.jsonl*"))
	for _, path := range files {
		input, err := os.Open(path)
		if err != nil {
			continue
		}
		w, e := zw.Create(filepath.Base(path))
		if e != nil {
			input.Close()
			return fail(e)
		}
		scanner := bufio.NewScanner(io.LimitReader(input, 16<<20))
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for scanner.Scan() {
			var record map[string]any
			if json.Unmarshal(scanner.Bytes(), &record) != nil {
				continue
			}
			clean := map[string]any{}
			for _, k := range []string{"time", "component", "level", "category", "event", "id"} {
				if v, ok := record[k]; ok {
					clean[k] = v
				}
			}
			json.NewEncoder(w).Encode(clean)
		}
		input.Close()
	}
	if err = zw.Close(); err != nil {
		return fail(err)
	}
	if err = f.Sync(); err != nil {
		return fail(err)
	}
	if err = f.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	a.log.Event("diagnostics", "bundle_created", "", nil)
	return name, nil
}
