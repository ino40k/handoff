package diagnostic

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeImportFailureIsSavedBeforeFrontendStarts(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python console runtime unavailable")
	}
	dir := t.TempDir()
	log, err := NewLauncherLogger(dir)
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr := NewLogWriter(log, "stdout", "boot"), NewLogWriter(log, "stderr", "boot")
	cmd := exec.Command(python, "-c", "print('runtime starting'); import handoff_missing_dependency_for_test")
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err = cmd.Run()
	if err == nil {
		t.Fatal("Expected an early runtime import failure")
	}
	stdout.Flush()
	stderr.Flush()
	log.Event("startup", "runtime_exited", "boot", err)
	log.Close()
	contents, err := os.ReadFile(filepath.Join(dir, "launcher.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"runtime starting", "ModuleNotFoundError", "handoff_missing_dependency_for_test", "runtime_exited"} {
		if !strings.Contains(string(contents), expected) {
			t.Fatalf("Startup log lost %q", expected)
		}
	}
}

func TestLauncherCapturesSplitSecretsAndFinalTraceback(t *testing.T) {
	dir := t.TempDir()
	log, err := NewLauncherLogger(dir)
	if err != nil {
		t.Fatal(err)
	}
	w := NewLogWriter(log, "stderr", "run-id")
	secret := "handoff1." + strings.Repeat("a", 50)
	parts := []string{"Traceback: " + secret[:12], secret[12:] + "\nModuleNotFoundError: PySide6.QtOpenGL"}
	for _, part := range parts {
		w.Write([]byte(part))
	}
	w.Flush()
	log.Close()
	contents, err := os.ReadFile(filepath.Join(dir, "launcher.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), secret) || !strings.Contains(string(contents), "[redacted]") {
		t.Fatal("Secret was not redacted after joining fragments")
	}
	lines := strings.Split(strings.TrimSpace(string(contents)), "\n")
	if len(lines) != 2 {
		t.Fatalf("Wanted two complete lines, got %d", len(lines))
	}
	for _, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record["component"] != "launcher" || record["id"] != "run-id" || record["event"] != "runtime_stderr" {
			t.Fatal(record)
		}
	}
	if !strings.Contains(lines[1], "QtOpenGL") {
		t.Fatal("Final unterminated error was lost")
	}
}

func TestLauncherDiscardsOversizedLineAndRecovers(t *testing.T) {
	dir := t.TempDir()
	log, err := NewLauncherLogger(dir)
	if err != nil {
		t.Fatal(err)
	}
	w := NewLogWriter(log, "stderr", "run")
	w.Write([]byte(strings.Repeat("x", 65<<10)))
	if len(w.line) != 0 || !w.discard {
		t.Fatal("Unbounded output buffer")
	}
	w.Write([]byte("\nuseful error\n"))
	w.Flush()
	log.Close()
	contents, _ := os.ReadFile(filepath.Join(dir, "launcher.jsonl"))
	if strings.Contains(string(contents), strings.Repeat("x", 100)) || !strings.Contains(string(contents), "runtime_output_discarded") || !strings.Contains(string(contents), "useful error") {
		t.Fatal("Oversized line handling lost subsequent diagnostics")
	}
}

func TestLauncherRotationRetainsBackendLogs(t *testing.T) {
	dir := t.TempDir()
	backend := filepath.Join(dir, "backend.jsonl")
	os.WriteFile(backend, []byte("backend sentinel"), 0600)
	for i := 0; i < 7; i++ {
		path := filepath.Join(dir, "launcher.jsonl")
		os.WriteFile(path, make([]byte, 2<<20), 0600)
		log, err := NewLauncherLogger(dir)
		if err != nil {
			t.Fatal(err)
		}
		log.Event("startup", "start", "run", nil)
		log.Close()
	}
	contents, _ := os.ReadFile(backend)
	if string(contents) != "backend sentinel" {
		t.Fatal("Launcher rotation touched backend logs")
	}
	for i := 1; i <= 5; i++ {
		if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("launcher.jsonl.%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "launcher.jsonl.6")); !os.IsNotExist(err) {
		t.Fatal("Retention limit exceeded")
	}
}
