//go:build windows

package main

import (
	"fmt"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"
)

// Create an unpackaged desktop shortcut with the explicit AppUserModelID
// required by Windows toast notifications (Microsoft IShellLink sample).
type comObject struct{ vt *[32]uintptr }

func (o *comObject) call(index int, args ...uintptr) error {
	all := append([]uintptr{uintptr(unsafe.Pointer(o))}, args...)
	hr, _, _ := syscall.SyscallN(o.vt[index], all...)
	if int32(hr) < 0 {
		return fmt.Errorf("Windows shell HRESULT 0x%08x", uint32(hr))
	}
	return nil
}
func (o *comObject) release() { o.call(2) }
func (o *comObject) query(iid *windows.GUID) (*comObject, error) {
	var out *comObject
	e := o.call(0, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out)))
	return out, e
}
func createShortcut(exe string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	ole := windows.NewLazySystemDLL("ole32.dll")
	hr, _, _ := ole.NewProc("CoInitializeEx").Call(0, 2)
	if int32(hr) < 0 {
		return fmt.Errorf("initialize shell COM")
	}
	defer ole.NewProc("CoUninitialize").Call()
	clsid, _ := windows.GUIDFromString("{00021401-0000-0000-C000-000000000046}")
	iid, _ := windows.GUIDFromString("{000214F9-0000-0000-C000-000000000046}")
	var link *comObject
	hr, _, _ = ole.NewProc("CoCreateInstance").Call(uintptr(unsafe.Pointer(&clsid)), 0, 1, uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&link)))
	if int32(hr) < 0 {
		return fmt.Errorf("create shell link")
	}
	defer link.release()
	exeW, _ := windows.UTF16PtrFromString(exe)
	if e := link.call(20, uintptr(unsafe.Pointer(exeW))); e != nil {
		return e
	}
	work, _ := windows.UTF16PtrFromString(filepath.Dir(exe))
	if e := link.call(9, uintptr(unsafe.Pointer(work))); e != nil {
		return e
	}
	icon, _ := windows.UTF16PtrFromString(filepath.Join(filepath.Dir(exe), "assets", "handoff.ico"))
	if e := link.call(17, uintptr(unsafe.Pointer(icon)), 0); e != nil {
		return e
	}
	propertyIID, _ := windows.GUIDFromString("{886D8EEB-8CF2-4446-8D02-CDBA1DBDCF99}")
	store, e := link.query(&propertyIID)
	if e != nil {
		return e
	}
	defer store.release()
	keyGUID, _ := windows.GUIDFromString("{9F4C2855-9F79-4B39-A8D0-E1D42DE1D5F3}")
	property := struct {
		GUID windows.GUID
		PID  uint32
	}{keyGUID, 5}
	value, _ := windows.UTF16PtrFromString("handoff.desktop")
	variant := struct {
		VT       uint16
		Reserved [3]uint16
		Value    uintptr
		Extra    uintptr
	}{VT: 31, Value: uintptr(unsafe.Pointer(value))}
	if e = store.call(6, uintptr(unsafe.Pointer(&property)), uintptr(unsafe.Pointer(&variant))); e != nil {
		return e
	}
	runtime.KeepAlive(value)
	if e = store.call(7); e != nil {
		return e
	}
	persistIID, _ := windows.GUIDFromString("{0000010B-0000-0000-C000-000000000046}")
	persist, e := link.query(&persistIID)
	if e != nil {
		return e
	}
	defer persist.release()
	target := filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs", "handoff.lnk")
	if e = os.MkdirAll(filepath.Dir(target), 0700); e != nil {
		return e
	}
	targetW, _ := windows.UTF16PtrFromString(target)
	return persist.call(6, uintptr(unsafe.Pointer(targetW)), 1)
}
