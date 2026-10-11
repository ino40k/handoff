package diagnostic

import (
	"bytes"
	"fmt"
	"strings"
)

// LogWriter collects a runtime's output into complete, redacted log records.
// Use one writer per pipe and Flush after the child process has exited.
type LogWriter struct {
	log        *Logger
	stream, id string
	line       []byte
	discard    bool
}

func NewLogWriter(log *Logger, stream, id string) *LogWriter {
	return &LogWriter{log: log, stream: stream, id: id}
}

func (w *LogWriter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		end := bytes.IndexByte(p, '\n')
		part := p
		if end >= 0 {
			part = p[:end]
		}
		if !w.discard {
			if len(w.line)+len(part) > 64<<10 {
				w.line = nil
				w.discard = true
			} else {
				w.line = append(w.line, part...)
			}
		}
		if end < 0 {
			break
		}
		w.Flush()
		p = p[end+1:]
	}
	return n, nil
}

func (w *LogWriter) Flush() {
	if w.discard {
		w.log.Event("startup", "runtime_output_discarded", w.id, fmt.Errorf("%s line exceeded diagnostic limit", w.stream))
	} else if line := strings.TrimSpace(string(w.line)); line != "" {
		// Redact only after joining pipe fragments, including split secrets.
		if w.stream == "stdout" {
			w.log.write("startup", "runtime_stdout", w.id, "info", Redact(line), "detail")
		} else {
			w.log.Event("startup", "runtime_"+w.stream, w.id, fmt.Errorf("%s", line))
		}
	}
	w.line = nil
	w.discard = false
}
