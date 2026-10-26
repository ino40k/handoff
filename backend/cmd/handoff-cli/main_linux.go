//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/term"
	"golang.org/x/text/width"
	"handoff/internal/core"
	"handoff/internal/headless"
	"handoff/internal/network"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
	"unicode"
)

const help = `handoff cli — headless, approval-based file transfers
Usage: handoff-cli [--socket PATH] [--json] COMMAND [OPTIONS]

  status [--watch]                   Connectivity, requests and live progress
  peers                             Trusted peers, online status and route
  invite                            Create a secret single-use 15-minute invitation
  join                              Read invitation privately from terminal or stdin
  pairings                          Show requests, IDs and fingerprints
  approve ID | reject ID            Decide an incoming pairing request
  rename PEER NAME | revoke PEER     Rename locally or revoke future access
  send PEER PATH...                  Durably queue hashing; request approval
  pending | queue | history          List requests, queue or transfer history
  inspect ID [--conflicts] [--destination DIR]
                                    Review complete contents and conflicts
  accept ID [--destination DIR] [--overwrite --review TOKEN]
                                    Approve the whole reviewed manifest
  deny ID | pause ID | resume ID | cancel ID | retry ID
  move ID up|down                    Reorder outgoing queue
  remove ID                         Remove a terminal record; retain replay receipt
  jobs | job retry|cancel|remove ID         Inspect/recover source preparation jobs
  settings [--name NAME] [--receive-dir DIR]
  logs | diagnostics | version

Service administration (normally handled by installer):
  init --data-dir DIR --name NAME --receive-dir DIR
  serve [--data-dir DIR] [--socket PATH]
  sudo systemctl status|restart|stop handoff-cli

Paths are accessed as the configured non-root service account. Grant it access
explicitly. Only trusted administrators should have control socket access.
Ctrl-C stops monitoring; transfers keep running. Use -- before source paths
beginning with a dash. Guide: /usr/local/share/doc/handoff-cli/CLI.md
(or docs/CLI.md in the source repository).
`

type peer struct {
	ID, Name, Connection, RouteScope string
	Online                           bool
}
type entry struct {
	Path       string
	Dir        bool
	Size, Done int64
}
type transfer struct {
	ID, PeerID, Peer, Direction, Label, Status, Error, Destination string
	Approved, Paused                                               bool
	Size, Done, Order                                              int64
	FileCount, DirectoryCount                                      int
	Speed, Eta                                                     float64
	Entries                                                        []entry
	Created, Updated                                               time.Time
}
type snapshot struct {
	Version, DeviceID, NetworkError, StorageError, LogDir string
	Ready                                                 bool
	Settings                                              core.Settings
	Peers                                                 []peer
	Transfers                                             []transfer
	Pairings                                              []network.PairInfo
}
type options struct {
	values map[string]string
	flags  map[string]bool
	args   []string
}

func parse(args []string) (options, error) {
	o := options{values: map[string]string{}, flags: map[string]bool{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			o.args = append(o.args, args[i+1:]...)
			break
		}
		if a == "-h" {
			a = "--help"
		}
		if !strings.HasPrefix(a, "--") {
			o.args = append(o.args, a)
			continue
		}
		kv := strings.SplitN(a[2:], "=", 2)
		k := kv[0]
		switch k {
		case "json", "watch", "overwrite", "conflicts", "help":
			if len(kv) > 1 {
				return o, fmt.Errorf("--%s takes no value", k)
			}
			o.flags[k] = true
		case "socket", "data-dir", "name", "receive-dir", "destination", "review":
			if len(kv) == 2 {
				o.values[k] = kv[1]
			} else {
				i++
				if i >= len(args) {
					return o, fmt.Errorf("--%s requires a value", k)
				}
				o.values[k] = args[i]
			}
		default:
			return o, fmt.Errorf("unknown option --%s; use --help", k)
		}
	}
	return o, nil
}
func safe(s string) string {
	return strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) {
			return -1
		}
		return r
	}, s)
}
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "handoff-cli:", safe(core.Redact(err.Error())))
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string, out io.Writer) error {
	o, err := parse(args)
	if err != nil {
		return err
	}
	if len(o.args) == 0 || o.flags["help"] {
		fmt.Fprint(out, help)
		return nil
	}
	cmd, a := o.args[0], o.args[1:]
	allowed := map[string]string{"serve": "data-dir", "init": "data-dir,name,receive-dir", "inspect": "destination,conflicts", "accept": "destination,overwrite,review", "status": "watch", "settings": "name,receive-dir"}
	for k := range o.values {
		if k != "socket" && !strings.Contains(","+allowed[cmd]+",", ","+k+",") {
			return fmt.Errorf("--%s is not valid for %s", k, cmd)
		}
	}
	for k := range o.flags {
		if k != "json" && !strings.Contains(","+allowed[cmd]+",", ","+k+",") {
			return fmt.Errorf("--%s is not valid for %s", k, cmd)
		}
	}
	need := func(n int) error {
		if len(a) != n {
			return fmt.Errorf("%s expects %d argument(s); use --help", cmd, n)
		}
		return nil
	}
	socket := o.values["socket"]
	if socket == "" {
		socket = os.Getenv("HANDOFF_CLI_SOCKET")
	}
	if socket == "" {
		socket = headless.DefaultSocket
	}
	dir := o.values["data-dir"]
	if dir == "" {
		dir = headless.DefaultData
	}
	switch cmd {
	case "version":
		if err = need(0); err != nil {
			return err
		}
		fmt.Fprintf(out, "handoff cli %s · engine %s · protocol %d\n", headless.Version, core.Version, network.Protocol)
		return nil
	case "serve":
		if err = need(0); err != nil {
			return err
		}
		if os.Geteuid() == 0 {
			return errors.New("the transfer service must run as a non-root account; use the systemd installer")
		}
		return headless.Serve(ctx, dir, socket)
	case "init":
		if err = need(0); err != nil {
			return err
		}
		if os.Geteuid() == 0 {
			return errors.New("initialize as the non-root service account; the installer handles this with runuser")
		}
		return headless.Init(dir, o.values["name"], o.values["receive-dir"])
	}
	call := func(method string, params, out any) error { return headless.Call(ctx, socket, method, params, out) }
	emit := func(v any) error { enc := json.NewEncoder(out); enc.SetIndent("", "  "); return enc.Encode(v) }
	if cmd == "invite" {
		if err = need(0); err != nil {
			return err
		}
		var v map[string]string
		if err = call("invite", nil, &v); err != nil {
			return err
		}
		if o.flags["json"] {
			return emit(v)
		}
		fmt.Fprintln(out, v["code"])
		return nil
	}
	if cmd == "join" {
		if err = need(0); err != nil {
			return errors.New("join reads the invitation from stdin; keep secrets out of shell history")
		}
		var code string
		if term.IsTerminal(int(os.Stdin.Fd())) {
			fmt.Fprint(os.Stderr, "Invitation (hidden): ")
			b, e := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
			if e != nil {
				return e
			}
			code = string(b)
		} else {
			b, e := io.ReadAll(io.LimitReader(os.Stdin, 8193))
			if e != nil {
				return e
			}
			code = string(b)
		}
		if err = call("join", map[string]string{"code": strings.TrimSpace(code)}, nil); err != nil {
			return err
		}
		fmt.Fprintln(out, "Pairing requested; the invitation owner must approve. Check pairings.")
		return nil
	}
	var s snapshot
	if err = call("summary", nil, &s); err != nil {
		return err
	}
	peerID := func(q string) (string, error) {
		var exact, prefix []string
		for _, p := range s.Peers {
			if p.ID == q {
				return p.ID, nil
			}
			if strings.EqualFold(p.Name, q) {
				exact = append(exact, p.ID)
			}
			if strings.HasPrefix(p.ID, q) {
				prefix = append(prefix, p.ID)
			}
		}
		if len(exact) == 1 {
			return exact[0], nil
		}
		if len(exact) == 0 && len(prefix) == 1 {
			return prefix[0], nil
		}
		return "", errors.New("unknown or ambiguous peer; use an ID from peers")
	}
	transferID := func(q string) (string, error) {
		var ids []string
		for _, t := range s.Transfers {
			if t.ID == q {
				return t.ID, nil
			}
			if strings.HasPrefix(t.ID, q) {
				ids = append(ids, t.ID)
			}
		}
		if len(ids) == 1 {
			return ids[0], nil
		}
		return "", errors.New("unknown or ambiguous transfer; check pending, queue or history")
	}
	switch cmd {
	case "status":
		if err = need(0); err != nil {
			return err
		}
		return watch(ctx, socket, o.flags["watch"], o.flags["json"], out)
	case "peers":
		if err = need(0); err != nil {
			return err
		}
		if o.flags["json"] {
			return emit(s.Peers)
		}
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tDEVICE\tSTATUS\tROUTE")
		for _, p := range s.Peers {
			online := "offline"
			if p.Online {
				online = "online"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s %s\n", p.ID, safe(p.Name), online, p.Connection, p.RouteScope)
		}
		return w.Flush()
	case "pairings":
		if err = need(0); err != nil {
			return err
		}
		if o.flags["json"] {
			return emit(s.Pairings)
		}
		for _, p := range s.Pairings {
			fmt.Fprintf(out, "%s  %s  %s  fingerprint %s  expires %s\n", p.ID, safe(p.Name), p.Status, p.Fingerprint, p.Expires.Format(time.RFC3339))
		}
		return nil
	case "approve", "reject":
		if err = need(1); err != nil {
			return err
		}
		var ids []string
		for _, p := range s.Pairings {
			if strings.HasPrefix(p.ID, a[0]) {
				ids = append(ids, p.ID)
			}
		}
		if len(ids) != 1 {
			return errors.New("unknown or ambiguous pairing ID; check pairings")
		}
		return call("pairDecision", map[string]any{"id": ids[0], "approve": cmd == "approve"}, nil)
	case "rename", "revoke":
		n := 1
		if cmd == "rename" {
			n = 2
		}
		if err = need(n); err != nil {
			return err
		}
		id, e := peerID(a[0])
		if e != nil {
			return e
		}
		if cmd == "revoke" {
			return call("removePeer", map[string]string{"id": id}, nil)
		}
		return call("renamePeer", map[string]string{"id": id, "name": a[1]}, nil)
	case "send":
		if len(a) < 2 {
			return errors.New("usage: handoff-cli send PEER PATH...")
		}
		id, e := peerID(a[0])
		if e != nil {
			return e
		}
		paths := make([]string, len(a)-1)
		for i, p := range a[1:] {
			paths[i], e = filepath.Abs(p)
			if e != nil {
				return e
			}
		}
		var job headless.Job
		if err = call("send", map[string]any{"peers": []string{id}, "paths": paths}, &job); err != nil {
			return err
		}
		if o.flags["json"] {
			return emit(job)
		}
		fmt.Fprintf(out, "Preparation queued: %s\nHashing continues in the service. Use status or jobs; the recipient must approve.\n", job.ID)
		return nil
	case "jobs":
		if err = need(0); err != nil {
			return err
		}
		var jobs []headless.Job
		if err = call("jobs", nil, &jobs); err != nil {
			return err
		}
		if o.flags["json"] {
			return emit(jobs)
		}
		for _, j := range jobs {
			fmt.Fprintf(out, "%s  %s  %s  %s\n", j.ID, j.Status, safe(strings.Join(j.Paths, ", ")), safe(j.Error))
		}
		return nil
	case "job":
		if err = need(2); err != nil {
			return err
		}
		return call("job", map[string]string{"action": a[0], "id": a[1]}, nil)
	case "pending", "queue", "history":
		if err = need(0); err != nil {
			return err
		}
		items := []transfer{}
		for _, t := range s.Transfers {
			if cmd == "queue" && !core.Terminal(t.Status) || cmd == "history" && (core.Terminal(t.Status) || t.Status == "failed") || cmd == "pending" && t.Direction == "incoming" && t.Status == "pending" {
				items = append(items, t)
			}
		}
		if o.flags["json"] {
			return emit(items)
		}
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tDIRECTION\tPEER\tNAME\tSTATUS\tPROGRESS")
		for _, t := range items {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s / %s\n", t.ID, t.Direction, safe(t.Peer), safe(t.Label), t.Status, bytes(t.Done), bytes(t.Size))
			if t.Error != "" {
				fmt.Fprintf(w, "\t%s\n", safe(t.Error))
			}
		}
		return w.Flush()
	case "inspect":
		if err = need(1); err != nil {
			return err
		}
		id, e := transferID(a[0])
		if e != nil {
			return e
		}
		var full snapshot
		if err = call("inspect", map[string]string{"id": id}, &full); err != nil {
			return err
		}
		var t transfer
		for _, v := range full.Transfers {
			if v.ID == id {
				t = v
			}
		}
		var review *core.ConflictReview
		if o.flags["conflicts"] || o.values["destination"] != "" {
			review = &core.ConflictReview{}
			if err = call("review", map[string]string{"id": id, "destination": o.values["destination"]}, review); err != nil {
				return err
			}
		}
		if o.flags["json"] {
			return emit(map[string]any{"transfer": t, "conflicts": review})
		}
		files, dirs := 0, 0
		for _, e := range t.Entries {
			if e.Dir {
				dirs++
			} else {
				files++
			}
		}
		dest := t.Destination
		if dest == "" {
			dest = s.Settings.ReceiveDir
		}
		if review != nil {
			dest = review.Destination
		}
		fmt.Fprintf(out, "%s · %s · %s\nPeer: %s (%s)\n%d files, %d directories · %s (%d bytes)\nDestination: %s\nCreated: %s · Updated: %s\n", t.ID, t.Direction, t.Status, safe(t.Peer), t.PeerID, files, dirs, bytes(t.Size), t.Size, safe(dest), t.Created.Format(time.RFC3339), t.Updated.Format(time.RFC3339))
		for _, e := range t.Entries {
			kind := "file"
			if e.Dir {
				kind = "dir"
			}
			decision := ""
			if review != nil {
				d := review.Files[e.Path]
				if d.Identical {
					decision = " [identical: skip]"
				} else if d.Hash != "" {
					decision = " [DIFFERENT: overwrite approval required]"
				}
			}
			fmt.Fprintf(out, "  %s  %s  %d bytes%s\n", kind, safe(e.Path), e.Size, decision)
		}
		if review != nil {
			fmt.Fprintf(out, "Stage needs %s; available %s. Replaced originals remain in .handoff-partials/%s-replaced/.\nReview token: %s\n", bytes(review.Required), bytes(int64(review.Available)), id, review.Token)
		}
		return nil
	case "accept":
		if err = need(1); err != nil {
			return err
		}
		id, e := transferID(a[0])
		if e != nil {
			return e
		}
		var review core.ConflictReview
		if err = call("review", map[string]string{"id": id, "destination": o.values["destination"]}, &review); err != nil {
			return err
		}
		token := o.values["review"]
		if o.flags["overwrite"] && token == "" {
			return errors.New("inspect ID --conflicts first; approve the reviewed batch with accept ID --overwrite --review TOKEN")
		}
		if token == "" {
			for _, d := range review.Files {
				if d.Hash != "" && !d.Identical {
					return errors.New("destination files differ; run inspect ID --conflicts, then accept ID --overwrite --review TOKEN")
				}
			}
			token = review.Token
		}
		if err = call("acceptReviewed", map[string]any{"id": id, "destination": review.Destination, "token": token, "overwrite": o.flags["overwrite"]}, nil); err != nil {
			return err
		}
		fmt.Fprintln(out, "Whole transfer approved. Identical files will be verified and skipped.")
		return nil
	case "deny", "pause", "resume", "cancel", "retry", "remove", "move":
		n := 1
		if cmd == "move" {
			n = 2
		}
		if err = need(n); err != nil {
			return err
		}
		id, e := transferID(a[0])
		if e != nil {
			return e
		}
		if cmd == "remove" {
			return call("removeRecord", map[string]string{"id": id}, nil)
		}
		if cmd == "move" {
			delta := 0
			if a[1] == "up" {
				delta = -1
			} else if a[1] == "down" {
				delta = 1
			} else {
				return errors.New("move direction must be up or down")
			}
			return call("reorder", map[string]any{"id": id, "delta": delta}, nil)
		}
		action := cmd
		if cmd == "deny" {
			action = "decline"
		}
		return call("transfer", map[string]string{"id": id, "action": action}, nil)
	case "settings":
		if err = need(0); err != nil {
			return err
		}
		settings := s.Settings
		_, nameSet := o.values["name"]
		_, dirSet := o.values["receive-dir"]
		if nameSet {
			settings.Name = o.values["name"]
		}
		if dirSet {
			settings.ReceiveDir = o.values["receive-dir"]
		}
		if nameSet || dirSet {
			if err = call("settings", map[string]any{"settings": settings}, nil); err != nil {
				return err
			}
		}
		if o.flags["json"] {
			return emit(settings)
		}
		fmt.Fprintf(out, "Device: %s\nReceive directory: %s\nIdentity: %s\n", safe(settings.Name), safe(settings.ReceiveDir), s.DeviceID)
		return nil
	case "logs":
		if err = need(0); err != nil {
			return err
		}
		if o.flags["json"] {
			return emit(map[string]string{"directory": s.LogDir, "journal": "journalctl -u handoff-cli -f"})
		}
		fmt.Fprintf(out, "Rotating logs: %s\nJournal: sudo journalctl -u handoff-cli -f\nRead log files as the service account or root.\n", safe(s.LogDir))
		return nil
	case "diagnostics":
		if err = need(0); err != nil {
			return err
		}
		var v any
		if err = call("diagnostics", nil, &v); err != nil {
			return err
		}
		return emit(v)
	default:
		return fmt.Errorf("unknown command %q; use --help", cmd)
	}
}
func bytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	v := float64(n)
	for _, u := range []string{"KiB", "MiB", "GiB", "TiB", "PiB"} {
		v /= 1024
		if v < 1024 {
			return fmt.Sprintf("%.1f %s", v, u)
		}
	}
	return fmt.Sprintf("%.1f PiB", v)
}
func watch(ctx context.Context, socket string, repeat, jsonOut bool, out io.Writer) error {
	tty := false
	if f, ok := out.(*os.File); ok {
		tty = term.IsTerminal(int(f.Fd()))
	}
	previousLines := 0
	last := ""
	delay := time.Second
	if !tty {
		delay = 10 * time.Second
	}
	if jsonOut {
		delay = 5 * time.Second
	}
	for {
		var s snapshot
		if err := headless.Call(ctx, socket, "summary", nil, &s); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		var jobs []headless.Job
		if err := headless.Call(ctx, socket, "jobs", nil, &jobs); err != nil {
			return err
		}
		if jsonOut {
			if err := json.NewEncoder(out).Encode(map[string]any{"status": s, "jobs": jobs}); err != nil {
				return err
			}
		} else {
			var b strings.Builder
			online, pending, queued, preparing := 0, 0, 0, 0
			for _, p := range s.Peers {
				if p.Online {
					online++
				}
			}
			for _, t := range s.Transfers {
				if t.Direction == "incoming" && t.Status == "pending" {
					pending++
				}
				if !core.Terminal(t.Status) {
					queued++
				}
			}
			for _, j := range jobs {
				if j.Status == "preparing" {
					preparing++
				}
			}
			net := "connecting"
			if s.Ready {
				net = "online"
			}
			fmt.Fprintf(&b, "%s · network %s · peers %d/%d · pending %d · queue %d · preparing %d\n", safe(s.Settings.Name), net, online, len(s.Peers), pending, queued, preparing)
			if s.StorageError != "" {
				fmt.Fprintln(&b, safe(s.StorageError))
			}
			if s.NetworkError != "" {
				fmt.Fprintln(&b, safe(s.NetworkError))
			}
			sort.SliceStable(s.Transfers, func(i, j int) bool { return s.Transfers[i].Status == "active" && s.Transfers[j].Status != "active" })
			shown := 0
			for _, t := range s.Transfers {
				if core.Terminal(t.Status) {
					continue
				}
				if shown >= 4 {
					break
				}
				shown++
				route := "offline"
				for _, p := range s.Peers {
					if p.ID == t.PeerID {
						route = p.Connection + " " + p.RouteScope
					}
				}
				eta := "—"
				if t.Speed > 0 && t.Eta > 0 {
					eta = (time.Duration(t.Eta) * time.Second).Round(time.Minute).String()
					if t.Eta < 60 {
						eta = "<1m"
					}
				}
				fmt.Fprintf(&b, "%s %.8s · %s · %s · %s · %s\n", t.Direction, t.ID, t.Status, route, safe(t.Peer), safe(t.Label))
				fmt.Fprintf(&b, "  Overall %s / %s · %s/s · ETA %s\n", bytes(t.Done), bytes(t.Size), bytes(int64(t.Speed)), eta)
				for _, e := range t.Entries {
					if !e.Dir && e.Done < e.Size {
						fmt.Fprintf(&b, "  %s · %s / %s\n", safe(e.Path), bytes(e.Done), bytes(e.Size))
						break
					}
				}
				if t.Error != "" {
					fmt.Fprintln(&b, "  "+safe(t.Error))
				}
			}
			text := b.String()
			if text != last || !repeat {
				if tty && previousLines > 0 {
					fmt.Fprintf(out, "\x1b[%dA", previousLines)
				}
				lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
				for _, line := range lines {
					if tty {
						fmt.Fprint(out, "\r\x1b[2K")
					}
					if tty {
						if f, ok := out.(*os.File); ok {
							if cols, _, err := term.GetSize(int(f.Fd())); err == nil {
								line = clip(line, cols-1)
							}
						}
					}
					fmt.Fprintln(out, line)
				}
				if tty && previousLines > len(lines) {
					for i := len(lines); i < previousLines; i++ {
						fmt.Fprint(out, "\r\x1b[2K\n")
					}
					fmt.Fprintf(out, "\x1b[%dA", previousLines-len(lines))
				}
				previousLines = len(lines)
				last = text
			}
		}
		if !repeat {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(delay):
		}
	}
}

// Keep refreshed output to one physical terminal row, including wide filenames.
func clip(s string, cols int) string {
	if cols < 2 {
		return ""
	}
	used := 0
	var b strings.Builder
	for _, r := range s {
		n := 1
		if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) {
			n = 0
		}
		switch width.LookupRune(r).Kind() {
		case width.EastAsianWide, width.EastAsianFullwidth:
			n = 2
		}
		if used+n > cols-1 {
			return b.String() + "…"
		}
		used += n
		b.WriteRune(r)
	}
	return b.String()
}
