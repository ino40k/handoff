//go:build windows

package main

import (
	"crypto/rand"
	"encoding/hex"
	"golang.org/x/sys/windows"
	"handoff/internal/diagnostic"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"unsafe"
)

func main() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "--register-shortcut" {
		if createShortcut(exe) != nil {
			os.Exit(1)
		}
		return
	}
	dir := filepath.Dir(exe)
	data := os.Getenv("HANDOFF_DATA_DIR")
	if data == "" {
		cache, cacheErr := os.UserCacheDir()
		if cacheErr != nil {
			cache = os.TempDir()
		}
		data = filepath.Join(cache, "handoff")
	}
	logDir := filepath.Join(data, "logs")
	log, logErr := diagnostic.NewLauncherLogger(logDir)
	var runID [16]byte
	rand.Read(runID[:])
	id := hex.EncodeToString(runID[:])
	if logErr == nil {
		defer log.Close()
		log.Event("startup", "launcher_start", id, nil)
	}
	args := append([]string{filepath.Join(dir, "frontend", "main.py")}, os.Args[1:]...)
	// A hidden console interpreter retains usable stderr even when Python
	// fails before Qt and frontend logging have been imported.
	cmd := exec.Command(filepath.Join(dir, "runtime", "python.exe"), args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "HANDOFF_LAUNCHER="+exe, "HANDOFF_STARTUP_ID="+id)
	if logErr == nil {
		stdout, stderr := diagnostic.NewLogWriter(log, "stdout", id), diagnostic.NewLogWriter(log, "stderr", id)
		cmd.Stdout, cmd.Stderr = stdout, stderr
		err = cmd.Run()
		stdout.Flush()
		stderr.Flush()
		log.Event("startup", "runtime_exited", id, err)
	} else {
		err = cmd.Run()
	}
	if err != nil {
		message := "handoff could not start. The startup error is saved in:\n" + filepath.Join(logDir, "launcher.jsonl") + "\n\n" + err.Error()
		if logErr != nil {
			message = "handoff could not start, and could not open its startup log:\n" + logErr.Error() + "\n\n" + err.Error()
		}
		text, _ := windows.UTF16PtrFromString(diagnostic.Redact(message))
		title, _ := windows.UTF16PtrFromString("handoff")
		windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), 0x10)
		os.Exit(1)
	}
}
