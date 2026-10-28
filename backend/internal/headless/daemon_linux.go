//go:build linux

// Package headless hosts the shared desktop engine without graphical dependencies.
package headless

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"handoff/internal/control"
	"handoff/internal/core"
	"handoff/internal/network"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const Version = "1.0.1"
const DefaultSocket = "/run/handoff-cli/control.sock"
const DefaultData = "/var/lib/handoff-cli"

type Reply struct {
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error,omitempty"`
}
type Job struct {
	ID      string    `json:"id"`
	Paths   []string  `json:"paths"`
	Peers   []string  `json:"peers"`
	Status  string    `json:"status"`
	Error   string    `json:"error,omitempty"`
	Created time.Time `json:"created"`
}
type Daemon struct {
	App    *control.App
	mu     sync.Mutex
	jobs   []Job
	active map[string]context.CancelFunc
	ctx    context.Context
}

func Lock(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "service.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("this data directory is already in use")
	}
	return f, nil
}
func Init(dir, name, receive string) error {
	if !network.ValidName(name) || !filepath.IsAbs(receive) {
		return errors.New("provide a device name and an absolute receiving directory")
	}
	lock, err := Lock(dir)
	if err != nil {
		return err
	}
	defer lock.Close()
	a, err := control.Open(dir, receive)
	if err != nil {
		return err
	}
	defer a.Close()
	if a.Store.Snapshot().Settings.Configured {
		return errors.New("already initialized; use settings to change preferences")
	}
	root, err := os.OpenRoot(receive)
	if err != nil {
		return err
	}
	defer root.Close()
	f, err := root.Create(".handoff-write-test-" + core.RandomID())
	if err != nil {
		return fmt.Errorf("receiving directory is not writable by the service account: %w", err)
	}
	n := filepath.Base(f.Name())
	f.Close()
	root.Remove(n)
	return a.Store.Update(func(s *core.State) error {
		s.Settings.Name = name
		s.Settings.ReceiveDir = receive
		s.Settings.Configured = true
		return nil
	})
}
func Serve(ctx context.Context, dir, socket string) error {
	os.Unsetenv("TS_DEBUG_ADDR")
	lock, err := Lock(dir)
	if err != nil {
		return err
	}
	defer lock.Close()
	a, err := control.Open(dir, filepath.Join(dir, "received"))
	if err != nil {
		return err
	}
	defer a.Close()
	if !a.Store.Snapshot().Settings.Configured {
		return errors.New("not configured; run handoff-cli init as the service account first")
	}
	if err = a.Store.EnableJournal(); err != nil {
		return err
	}
	a.Log.Event("recovery", "progress_journal_ready", "", nil)
	d := &Daemon{App: a, ctx: ctx, active: map[string]context.CancelFunc{}}
	data, err := os.ReadFile(filepath.Join(dir, "jobs.json"))
	if err == nil {
		if err = json.Unmarshal(data, &d.jobs); err != nil {
			return errors.New("cannot decode durable preparation jobs")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(socket)
	if err = os.MkdirAll(parent, 0750); err != nil {
		return err
	}
	st, err := os.Lstat(parent)
	if err != nil {
		return err
	}
	if !st.IsDir() || st.Mode()&0022 != 0 {
		return errors.New("control socket directory must not be writable by group or other users")
	}
	// The exclusive state lock makes removal of our stale socket safe. Refuse files/links.
	if st, err = os.Lstat(socket); err == nil {
		if st.Mode()&os.ModeSocket == 0 {
			return errors.New("control socket path contains another file")
		}
		if conn, e := net.DialTimeout("unix", socket, 200*time.Millisecond); e == nil {
			conn.Close()
			return errors.New("control socket is already in use by another profile")
		}
		if err = os.Remove(socket); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		return err
	}
	defer ln.Close()
	if err = os.Chmod(socket, 0660); err != nil {
		return err
	}
	for _, job := range d.jobs {
		if job.Status == "canceled" {
			if err = d.cancelPrepared(job); err != nil {
				return err
			}
		}
	}
	go a.Service.Run()
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); d.prepareLoop() }()
	go func() { <-ctx.Done(); ln.Close(); a.Service.Close() }()
	var wg sync.WaitGroup
	defer func() { a.Service.Close(); wg.Wait(); <-workerDone }()
	sem := make(chan struct{}, 16)
	a.Log.Event("service", "control_ready", "", nil)
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			conn.Close()
			return nil
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			defer conn.Close()
			defer func() {
				if recover() != nil {
					a.Log.Event("exception", "control_panic", "", errors.New("unexpected command panic"))
				}
			}()
			conn.SetReadDeadline(time.Now().Add(15 * time.Second))
			scanner := bufio.NewScanner(conn)
			scanner.Buffer(make([]byte, 4096), 1<<20)
			if !scanner.Scan() {
				return
			}
			var req control.Request
			if json.Unmarshal(scanner.Bytes(), &req) != nil {
				json.NewEncoder(conn).Encode(map[string]string{"error": "invalid local request"})
				return
			}
			result, err := d.command(req)
			response := map[string]any{"result": result}
			if err != nil {
				a.Log.Event("cli", req.Method+"_failed", req.ID, err)
				response["error"] = core.Redact(err.Error())
			}
			conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
			json.NewEncoder(conn).Encode(response)
		}()
	}
}
func (d *Daemon) saveJobs() error {
	b, err := json.Marshal(d.jobs)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(d.App.Dir, ".jobs-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err != nil {
		return err
	}
	if ce != nil {
		return ce
	}
	if err = os.Rename(f.Name(), filepath.Join(d.App.Dir, "jobs.json")); err != nil {
		return err
	}
	p, err := os.Open(d.App.Dir)
	if err != nil {
		return err
	}
	defer p.Close()
	return p.Sync()
}
func (d *Daemon) prepareLoop() {
	for d.ctx.Err() == nil {
		d.mu.Lock()
		var job *Job
		for i := range d.jobs {
			if d.jobs[i].Status == "preparing" {
				copy := d.jobs[i]
				job = &copy
				break
			}
		}
		var workCtx context.Context
		var workCancel context.CancelFunc
		if job != nil {
			workCtx, workCancel = context.WithCancel(d.ctx)
			d.active[job.ID] = workCancel
		}
		d.mu.Unlock()
		if job != nil {
			d.App.Log.Event("queue", "preparation_started", job.ID, nil)
			err := d.App.Service.SendWithID(workCtx, job.Paths, job.Peers, job.ID)
			workCancel()
			if d.ctx.Err() != nil {
				return
			}
			d.mu.Lock()
			delete(d.active, job.ID)
			for i := range d.jobs {
				if d.jobs[i].ID == job.ID {
					if d.jobs[i].Status == "canceled" {
						err = errors.Join(err, d.cancelPrepared(d.jobs[i]))
						continue
					}
					d.jobs[i].Status = "queued"
					d.jobs[i].Error = ""
					if err != nil {
						d.jobs[i].Status = "failed"
						d.jobs[i].Error = core.Redact(err.Error())
					}
				}
			}
			se := d.saveJobs()
			d.mu.Unlock()
			d.App.Log.Event("queue", "preparation_finished", job.ID, errors.Join(err, se))
		}
		select {
		case <-d.ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}
func (d *Daemon) command(req control.Request) (any, error) {
	var p struct {
		ID, Destination, Token, Action string
		Overwrite                      bool
		Paths, Peers                   []string
	}
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
	}
	if req.Method != "snapshot" && req.Method != "summary" && req.Method != "jobs" {
		d.App.Log.Event("cli", req.Method, req.ID, nil)
	}
	switch req.Method {
	case "summary":
		return d.App.Summary(), nil
	case "inspect":
		return d.App.Inspect(p.ID), nil
	case "jobs":
		d.mu.Lock()
		defer d.mu.Unlock()
		return append([]Job{}, d.jobs...), nil
	case "send":
		if len(p.Paths) == 0 || len(p.Peers) == 0 {
			return nil, errors.New("provide a peer and source paths")
		}
		st := d.App.Store.Snapshot()
		for _, id := range p.Peers {
			if st.Peers[id] == nil {
				return nil, errors.New("unknown peer")
			}
		}
		for _, path := range p.Paths {
			if !filepath.IsAbs(path) {
				return nil, errors.New("source paths must be absolute")
			}
			if _, err := os.Lstat(path); err != nil {
				return nil, fmt.Errorf("service account cannot read source: %w", err)
			}
		}
		job := Job{ID: core.RandomID(), Paths: p.Paths, Peers: p.Peers, Status: "preparing", Created: time.Now().UTC()}
		d.mu.Lock()
		defer d.mu.Unlock()
		d.jobs = append(d.jobs, job)
		if err := d.saveJobs(); err != nil {
			d.jobs = d.jobs[:len(d.jobs)-1]
			return nil, err
		}
		return job, nil
	case "job":
		d.mu.Lock()
		defer d.mu.Unlock()
		for i := range d.jobs {
			if d.jobs[i].ID == p.ID {
				job := d.jobs[i]
				switch p.Action {
				case "cancel":
					if job.Status != "preparing" && job.Status != "failed" {
						return nil, errors.New("preparation already finished; use cancel with a transfer ID from queue")
					}
					d.jobs[i].Status = "canceled"
					d.jobs[i].Error = ""
					if err := d.saveJobs(); err != nil {
						d.jobs[i] = job
						return nil, err
					}
					if cancel := d.active[p.ID]; cancel != nil {
						cancel()
					}
					return nil, d.cancelPrepared(job)
				case "retry":
					if job.Status != "failed" {
						return nil, errors.New("only failed preparation jobs can be retried")
					}
					d.jobs[i].Status = "preparing"
					d.jobs[i].Error = ""
				case "remove":
					if job.Status == "preparing" || d.active[p.ID] != nil {
						return nil, errors.New("preparation still running; cancel it first and wait for the worker to stop")
					}
					if job.Status == "canceled" {
						if err := d.cancelPrepared(job); err != nil {
							return nil, err
						}
					}
					d.jobs = append(d.jobs[:i], d.jobs[i+1:]...)
				default:
					return nil, errors.New("unknown job action")
				}
				return nil, d.saveJobs()
			}
		}
		return nil, errors.New("unknown preparation job")
	case "review":
		return d.App.Service.Receiver.Review(p.ID, p.Destination)
	case "acceptReviewed":
		return nil, d.App.Service.Receiver.AcceptReviewed(p.ID, p.Destination, p.Token, p.Overwrite)
	case "removeRecord":
		return nil, d.App.Store.Update(func(st *core.State) error {
			t := st.Transfers[p.ID]
			if t == nil || !core.Terminal(t.Status) {
				return errors.New("only completed, declined or canceled records can be removed")
			}
			if t.Direction == "outgoing" && t.Status == "canceled" && !t.RemoteAck {
				return errors.New("waiting for remote cancellation acknowledgement")
			}
			if t.Direction == "incoming" {
				if st.Receipts == nil {
					st.Receipts = map[string]core.Receipt{}
				}
				st.Receipts[t.ID] = core.Receipt{PeerID: t.PeerID, Digest: t.Manifest.Digest, Status: t.Status, Approved: t.Approved}
			}
			delete(st.Transfers, p.ID)
			return nil
		})
	case "transfer":
		if p.Action == "accept" {
			return nil, errors.New("CLI approval requires a destination review")
		}
	case "quit":
		return nil, errors.New("use systemctl stop handoff-cli to stop the service")
	}
	return d.App.Command(req)
}
func Call(ctx context.Context, socket, method string, params any, out any) error {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return fmt.Errorf("cannot reach handoff-cli service: %w; check systemctl status handoff-cli and socket permissions", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if err = json.NewEncoder(conn).Encode(map[string]any{"id": core.RandomID(), "method": method, "params": params}); err != nil {
		return err
	}
	var reply Reply
	if err = json.NewDecoder(io.LimitReader(conn, 128<<20)).Decode(&reply); err != nil {
		return err
	}
	if reply.Error != "" {
		return errors.New(reply.Error)
	}
	if out != nil {
		return json.Unmarshal(reply.Result, out)
	}
	return nil
}

// Reconcile cancellation after a crash between manifest publication and updating
// its preparation job. Ordinary transfer cancellation retains remote receipts.
func (d *Daemon) cancelPrepared(job Job) error {
	for _, peer := range job.Peers {
		id := network.JobTransferID(job.ID, peer)
		t := d.App.Store.Snapshot().Transfers[id]
		if t != nil && !core.Terminal(t.Status) {
			if err := d.App.Service.LocalAction(id, "cancel", ""); err != nil {
				return err
			}
		}
	}
	return nil
}
