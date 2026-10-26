//go:build linux

package headless

import (
	"bytes"
	"context"
	"encoding/json"
	"handoff/internal/control"
	"handoff/internal/core"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// Exercise the actual desktop stdio host against the actual headless socket host.
// No substitute transport, tailnet, or real user profile is involved.
func TestDesktopCLIInteroperability(t *testing.T) {
	if os.Getenv("HANDOFF_NETWORK_TEST") != "1" {
		t.Skip("set HANDOFF_NETWORK_TEST=1 for real tailcat interoperability")
	}
	base := t.TempDir()
	binary := os.Getenv("HANDOFF_DESKTOP_ENGINE")
	if binary == "" {
		binary = filepath.Join(base, "handoff-engine")
		build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-trimpath", "-o", binary, "../../cmd/handoff-engine")
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build desktop host: %v: %s", err, output)
		}
	} else {
		var err error
		binary, err = filepath.Abs(binary)
		check(t, err)
		_, err = os.Stat(binary)
		check(t, err)
	}
	cliBinary := filepath.Join(base, "handoff-cli")
	buildCLI := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-trimpath", "-o", cliBinary, "../../cmd/handoff-cli")
	if output, err := buildCLI.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v: %s", err, output)
	}
	desktopDir := filepath.Join(base, "desktop")
	desktopReceive := filepath.Join(base, "desktop-receive")
	check(t, os.Mkdir(desktopReceive, 0700))
	ds, e := core.NewStore(desktopDir, desktopReceive)
	check(t, e)
	check(t, ds.Update(func(s *core.State) error {
		s.Settings.Name = "Desktop example"
		s.Settings.Configured = true
		return nil
	}))
	process := exec.Command(binary)
	process.Env = append(os.Environ(), "HANDOFF_DATA_DIR="+desktopDir, "HANDOFF_RECEIVE_DIR="+desktopReceive)
	stdin, e := process.StdinPipe()
	check(t, e)
	stdout, e := process.StdoutPipe()
	check(t, e)
	var stderr bytes.Buffer
	process.Stderr = &stderr
	check(t, process.Start())
	t.Cleanup(func() {
		stdin.Close()
		if err := process.Wait(); err != nil {
			t.Errorf("desktop host exit: %v", err)
		}
	})
	encoder, decoder := json.NewEncoder(stdin), json.NewDecoder(stdout)
	rpc := func(method string, params any) json.RawMessage {
		t.Helper()
		b, _ := json.Marshal(params)
		check(t, encoder.Encode(control.Request{ID: core.RandomID(), Method: method, Params: b}))
		var response Reply
		check(t, decoder.Decode(&response))
		if response.Error != "" {
			t.Fatal(method, response.Error)
		}
		return response.Result
	}
	cliDir, receive := filepath.Join(base, "cli"), filepath.Join(base, "cli-receive")
	check(t, os.Mkdir(receive, 0700))
	check(t, Init(cliDir, "CLI example", receive))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	socket := filepath.Join(base, "control.sock")
	go func() { done <- Serve(ctx, cliDir, socket) }()
	t.Cleanup(func() { cancel(); check(t, <-done) })
	cli := func(method string, p, out any) {
		t.Helper()
		check(t, Call(context.Background(), socket, method, p, out))
	}
	runCLI := func(args ...string) []byte {
		t.Helper()
		command := exec.Command(cliBinary, append([]string{"--socket", socket}, args...)...)
		var errout bytes.Buffer
		command.Stderr = &errout
		output, err := command.Output()
		if err != nil {
			t.Fatalf("CLI command failed: %v: %s", err, errout.String())
		}
		return output
	}
	type transferView struct {
		ID, Status, Direction string
		Done                  int64
		Entries               []struct{ Done int64 }
	}
	type view struct {
		Ready bool
		Peers []struct {
			ID, Name               string
			Online                 bool
			Connection, RouteScope string
		}
		Pairings  []struct{ ID, Status string }
		Transfers []transferView
	}
	snapshot := func() view { var v view; cli("snapshot", nil, &v); return v }
	until(t, func() bool { var v view; check(t, json.Unmarshal(rpc("snapshot", nil), &v)); return v.Ready })
	until(t, func() bool {
		var v view
		return Call(context.Background(), socket, "snapshot", nil, &v) == nil && v.Ready
	})
	var code map[string]string
	cli("invite", nil, &code)
	rpc("join", map[string]string{"code": code["code"]})
	var pairID string
	until(t, func() bool {
		for _, p := range snapshot().Pairings {
			if p.Status == "approval" {
				pairID = p.ID
				return true
			}
		}
		return false
	})
	if len(snapshot().Peers) != 0 {
		t.Fatal("paired without approval")
	}
	runCLI("approve", pairID)
	until(t, func() bool { v := snapshot(); return len(v.Peers) == 1 && v.Peers[0].Online })
	desktopID := snapshot().Peers[0].ID
	var dv view
	check(t, json.Unmarshal(rpc("snapshot", nil), &dv))
	if len(dv.Peers) != 1 {
		t.Fatal("desktop did not save reciprocal peer")
	}
	cliID := dv.Peers[0].ID
	source := filepath.Join(base, "Source tree")
	check(t, os.MkdirAll(filepath.Join(source, "empty"), 0700))
	payload := bytes.Repeat([]byte("synthetic transfer"), 300000)
	check(t, os.WriteFile(filepath.Join(source, "file ü.bin"), payload, 0600))
	rpc("send", map[string]any{"paths": []string{source}, "peers": []string{cliID}})
	var incoming string
	until(t, func() bool {
		for _, tr := range snapshot().Transfers {
			if tr.Direction == "incoming" && tr.Status == "pending" {
				incoming = tr.ID
				return true
			}
		}
		return false
	})
	for _, tr := range snapshot().Transfers {
		if tr.ID == incoming {
			for _, e := range tr.Entries {
				if e.Done != 0 {
					t.Fatal("received without approval")
				}
			}
		}
	}
	var review core.ConflictReview
	cli("review", map[string]string{"id": incoming}, &review)
	runCLI("inspect", incoming, "--conflicts")
	runCLI("accept", incoming)
	until(t, func() bool {
		for _, tr := range snapshot().Transfers {
			if tr.ID == incoming && tr.Status == "completed" {
				return true
			}
		}
		return false
	})
	got, e := os.ReadFile(filepath.Join(receive, "Source tree", "file ü.bin"))
	check(t, e)
	if !bytes.Equal(got, payload) {
		t.Fatal("desktop -> CLI content mismatch")
	}
	if _, e = os.Stat(filepath.Join(receive, "Source tree", "empty")); e != nil {
		t.Fatal("empty directory lost")
	}
	// The CLI's durable job sends the received tree back through the legacy host.
	var job Job
	check(t, json.Unmarshal(runCLI("send", desktopID, filepath.Join(receive, "Source tree"), "--json"), &job))
	var back string
	until(t, func() bool {
		check(t, json.Unmarshal(rpc("snapshot", nil), &dv))
		for _, tr := range dv.Transfers {
			if tr.Direction == "incoming" && tr.Status == "pending" {
				back = tr.ID
				return true
			}
		}
		return false
	})
	rpc("transfer", map[string]string{"id": back, "action": "accept"})
	until(t, func() bool {
		check(t, json.Unmarshal(rpc("snapshot", nil), &dv))
		for _, tr := range dv.Transfers {
			if tr.ID == back && tr.Status == "completed" {
				return true
			}
		}
		return false
	})
	f, e := os.Open(filepath.Join(desktopReceive, "Source tree", "file ü.bin"))
	check(t, e)
	got, e = io.ReadAll(f)
	f.Close()
	check(t, e)
	if !bytes.Equal(got, payload) {
		t.Fatal("CLI -> desktop content mismatch")
	}
	pr := snapshot().Peers[0]
	if os.Getenv("TS_DEBUG_ALWAYS_USE_DERP") == "true" && pr.Connection != "relayed" {
		t.Fatalf("forced relay did not report relay: %s", pr.Connection)
	}
	t.Logf("real tailcat: connection=%s scope=%s; recursive transfers passed in both directions", pr.Connection, pr.RouteScope)
	runCLI("peers")
	runCLI("status")
	runCLI("history")
	runCLI("revoke", desktopID)
	if len(snapshot().Peers) != 0 {
		t.Fatal("revocation did not remove peer")
	}
}
