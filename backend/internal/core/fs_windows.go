//go:build windows

package core

import (
	"fmt"
	"golang.org/x/sys/windows"
	"os"
)

// Windows uses the receiving account's inherited ACLs and the documented trusted
// same-user filesystem model; Unix ownership/mode checks do not apply.
func validateStageDirectory(root *os.Root) error { return nil }

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
func renameExclusive(source *os.File, from string, destination *os.File, to string) error {
	// Windows MoveFile does not replace an existing destination. Both directory
	// handles were opened through os.Root; source and destination names are single
	// components. Root pathname stability uses the trusted same-user model.
	a, e := windows.UTF16PtrFromString(source.Name() + string(os.PathSeparator) + from)
	if e != nil {
		return e
	}
	b, e := windows.UTF16PtrFromString(destination.Name() + string(os.PathSeparator) + to)
	if e != nil {
		return e
	}
	if e = windows.MoveFile(a, b); e != nil {
		return fmt.Errorf("finalize: %w", e)
	}
	return nil
}
