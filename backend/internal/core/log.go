package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var secretPatterns = regexp.MustCompile(`(?:tc[A-Za-z0-9_-]{24,}|(?:privkey:|nodekey:|psk:)[A-Za-z0-9]+|handoff1\.[A-Za-z0-9_-]+)`)

func Redact(s string) string { return secretPatterns.ReplaceAllString(s, "[redacted]") }

type Logger struct {
	mu  sync.Mutex
	dir string
	f   *os.File
}

func NewLogger(dir string) (*Logger, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	l := &Logger{dir: dir}
	var err error
	l.f, err = os.OpenFile(filepath.Join(dir, "backend.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	return l, err
}
func (l *Logger) Event(category, event, id string, err error) {
	level := "info"
	msg := ""
	if err != nil {
		level = "error"
		msg = Redact(err.Error())
	}
	l.write(category, event, id, level, msg, "error")
}
func (l *Logger) write(category, event, id, level, msg, field string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return
	}
	record := map[string]any{"time": time.Now().UTC().Format(time.RFC3339Nano), "component": "backend", "level": level, "category": category, "event": event, "id": id}
	if msg != "" {
		record[field] = msg
	}
	b, _ := json.Marshal(record)
	l.f.Write(append(b, '\n'))
	if st, e := l.f.Stat(); e == nil && st.Size() > 2<<20 {
		l.f.Sync()
		l.f.Close()
		os.Remove(filepath.Join(l.dir, "backend.jsonl.5"))
		for i := 4; i >= 1; i-- {
			os.Rename(filepath.Join(l.dir, fmt.Sprintf("backend.jsonl.%d", i)), filepath.Join(l.dir, fmt.Sprintf("backend.jsonl.%d", i+1)))
		}
		os.Rename(filepath.Join(l.dir, "backend.jsonl"), filepath.Join(l.dir, "backend.jsonl.1"))
		l.f, _ = os.OpenFile(filepath.Join(l.dir, "backend.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	}
}
func (l *Logger) Tailcat(format string, args ...any) { // upstream diagnostics are useful, but can contain secret addresses.
	msg := Redact(fmt.Sprintf(format, args...))
	l.write("tailcat", "network_detail", "", "debug", strings.TrimSpace(msg), "detail")
}
func (l *Logger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		l.f.Sync()
		l.f.Close()
		l.f = nil
	}
}

func (l *Logger) Progress(category, event, id string, verified, total int64) {
	l.write(category, event, id, "info", fmt.Sprintf("verified_bytes=%d total_bytes=%d", verified, total), "detail")
}
