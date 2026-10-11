//go:build windows

package core

import (
	"fmt"
	"golang.org/x/sys/windows"
	"os"
)

func secureDir(p string) error {
	u, e := windows.GetCurrentProcessToken().GetTokenUser()
	if e != nil {
		return e
	}
	sd, e := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + u.User.Sid.String() + ")")
	if e != nil {
		return e
	}
	dacl, _, e := sd.DACL()
	if e != nil {
		return e
	}
	return windows.SetNamedSecurityInfo(p, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}
func replaceFile(a, b string) error {
	ap, e := windows.UTF16PtrFromString(a)
	if e != nil {
		return e
	}
	bp, e := windows.UTF16PtrFromString(b)
	if e != nil {
		return e
	}
	return windows.MoveFileEx(ap, bp, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
func syncDir(p string) error { return nil } // state file is flushed; MoveFileEx requests write-through.
func available(p string) (uint64, error) {
	ptr, e := windows.UTF16PtrFromString(p)
	if e != nil {
		return 0, e
	}
	var free, total, all uint64
	e = windows.GetDiskFreeSpaceEx(ptr, &free, &total, &all)
	return free, e
}
func renameExclusive(root *os.File, from, to string) error {
	// Windows MoveFile does not replace an existing destination. Both paths were
	// created through os.Root; top-level destination names cannot contain slashes.
	a, e := windows.UTF16PtrFromString(root.Name() + string(os.PathSeparator) + from)
	if e != nil {
		return e
	}
	b, e := windows.UTF16PtrFromString(root.Name() + string(os.PathSeparator) + to)
	if e != nil {
		return e
	}
	if e = windows.MoveFile(a, b); e != nil {
		return fmt.Errorf("finalize: %w", e)
	}
	return nil
}
