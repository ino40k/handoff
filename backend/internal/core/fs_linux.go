//go:build linux

package core

import (
	"golang.org/x/sys/unix"
	"os"
)

func secureDir(p string) error      { return os.Chmod(p, 0700) }
func replaceFile(a, b string) error { return os.Rename(a, b) }
func syncDir(p string) error {
	f, e := os.Open(p)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func available(p string) (uint64, error) {
	var st unix.Statfs_t
	e := unix.Statfs(p, &st)
	return st.Bavail * uint64(st.Bsize), e
}
func renameExclusive(root *os.File, from, to string) error {
	return unix.Renameat2(int(root.Fd()), from, int(root.Fd()), to, unix.RENAME_NOREPLACE)
}
