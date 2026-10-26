package control

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
	"time"
)

type Request struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}
type App struct {
	Store       *core.Store
	Service     *network.Service
	Log         *core.Logger
	Dir, LogDir string
}

func (a *App) Command(req Request) (any, error) {
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
		return a.Snapshot(), nil
	case "invite":
		code, err := a.Service.CreateInvitation()
		return map[string]string{"code": code}, err
	case "join":
		return nil, a.Service.JoinInvitation(strings.TrimSpace(p.Code))
	case "pairDecision":
		return nil, a.Service.PairDecision(p.ID, p.Approve)
	case "removePeer":
		return nil, a.Service.RemovePeer(p.ID)
	case "renamePeer":
		if !network.ValidName(p.Name) {
			return nil, errors.New("name must contain 1–80 characters")
		}
		return nil, a.Store.Update(func(s *core.State) error {
			peer := s.Peers[p.ID]
			if peer == nil {
				return errors.New("unknown peer")
			}
			peer.Name = p.Name
			return nil
		})
	case "send":
		return nil, a.Service.Send(context.Background(), p.Paths, p.Peers)
	case "transfer":
		return nil, a.Service.LocalAction(p.ID, p.Action, p.Destination)
	case "reorder":
		return nil, a.Service.Reorder(p.ID, p.Delta)
	case "clearHistory":
		return nil, a.Service.ClearHistory()
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
		return nil, a.Store.Update(func(s *core.State) error { s.Settings = p.Settings; return nil })
	case "diagnostics":
		path, err := a.diagnostics()
		return map[string]string{"path": path}, err
	default:
		return nil, errors.New("unknown command")
	}
}
func (a *App) Snapshot() any         { return a.snapshot(true, "") }
func (a *App) Summary() any          { return a.snapshot(false, "") }
func (a *App) Inspect(id string) any { return a.snapshot(true, id) }
func (a *App) snapshot(details bool, only string) any {
	st := a.Store.Snapshot()
	pr := a.Service.Presence()
	rates := a.Service.Rates()
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
		if only != "" && t.ID != only {
			continue
		}
		entries := []any{}
		var done int64
		currentShown := false
		files, dirs := 0, 0
		for i, e := range t.Manifest.Entries {
			n := int64(0)
			if i < len(t.Done) {
				n = t.Done[i]
			}
			done += n
			if e.Dir {
				dirs++
			} else {
				files++
			}
			if details || (!currentShown && !e.Dir && n < e.Size) {
				currentShown = true
				entries = append(entries, map[string]any{"path": e.Path, "dir": e.Dir, "size": e.Size, "done": n})
			}
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
		transfers = append(transfers, map[string]any{"id": t.ID, "peerId": t.PeerID, "peer": name, "direction": t.Direction, "label": label, "status": t.Status, "approved": t.Approved, "paused": t.Paused, "size": t.Manifest.Total, "done": done, "speed": speed, "eta": eta, "error": t.Error, "destination": t.Destination, "entries": entries, "fileCount": files, "directoryCount": dirs, "created": t.Created, "updated": t.Updated, "order": t.Order, "final": t.Final})
	}
	ready, err := a.Service.Ready()
	storageError := ""
	if e := a.Store.Health(); e != nil {
		storageError = "State storage failed; free space/fix permissions and restart service: " + core.Redact(e.Error())
	}
	return map[string]any{"version": core.Version, "deviceId": core.PeerID(st.Identity.Key.Public()), "settings": st.Settings, "peers": peers, "transfers": transfers, "pairings": a.Service.Pairings(), "ready": ready, "networkError": err, "storageError": storageError, "logDir": a.LogDir}
}
func (a *App) diagnostics() (string, error) {
	name := filepath.Join(a.LogDir, "handoff-diagnostics-"+time.Now().UTC().Format("20060102-150405")+".zip")
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	zw := zip.NewWriter(f)
	fail := func(err error) (string, error) { zw.Close(); f.Close(); os.Remove(name); return "", err }
	st := a.Store.Snapshot()
	summary := map[string]any{"version": core.Version, "os": runtime.GOOS, "arch": runtime.GOARCH, "peers": len(st.Peers), "transfers": len(st.Transfers), "network": a.Service.Presence()}
	out, err := zw.Create("summary.json")
	if err != nil {
		return fail(err)
	}
	json.NewEncoder(out).Encode(summary)
	files, _ := filepath.Glob(filepath.Join(a.LogDir, "*.jsonl*"))
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
	a.Log.Event("diagnostics", "bundle_created", "", nil)
	return name, nil
}

// Open shares the same durable engine with the desktop and headless hosts.
func Open(dir, receive string) (*App, error) {
	logs := filepath.Join(dir, "logs")
	log, err := core.NewLogger(logs)
	if err != nil {
		return nil, err
	}
	store, err := core.NewStore(dir, receive)
	if err != nil {
		log.Close()
		return nil, err
	}
	return &App{Store: store, Service: network.New(store, log, dir), Log: log, Dir: dir, LogDir: logs}, nil
}
func (a *App) Close() { a.Service.Close(); a.Log.Close() }
