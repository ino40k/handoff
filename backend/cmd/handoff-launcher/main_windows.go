//go:build windows

package main

import (
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"path/filepath"
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
	args := append([]string{filepath.Join(dir, "frontend", "main.py")}, os.Args[1:]...)
	cmd := exec.Command(filepath.Join(dir, "runtime", "pythonw.exe"), args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "HANDOFF_LAUNCHER="+exe)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err = cmd.Run(); err != nil {
		text, _ := windows.UTF16PtrFromString("handoff could not start. Try running runtime\\python.exe frontend\\main.py from this folder to see the error.\n\n" + err.Error())
		title, _ := windows.UTF16PtrFromString("handoff")
		windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), 0x10)
		os.Exit(1)
	}
}
