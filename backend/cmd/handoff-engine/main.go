package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"handoff/internal/control"
	"handoff/internal/core"
	"handoff/internal/network"
	"os"
	"path/filepath"
	"sync"
)

type request = control.Request
type app struct {
	controller *control.App
	log        *core.Logger
	out        sync.Mutex
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
	a := &app{controller: &control.App{Store: store, Service: service, Log: log, Dir: dir, LogDir: logs}, log: log}
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
			result, err := a.controller.Command(req)
			a.reply(req.ID, result, err)
		}(req)
	}
	service.Close()
	wg.Wait()
	if scanner.Err() != nil {
		log.Event("ipc", "input_failed", "", scanner.Err())
	}
}
