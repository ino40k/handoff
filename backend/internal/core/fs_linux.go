//go:build linux

package core

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
)

func validateStageDirectory(root *os.Root) error {
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	var st unix.Stat_t
	if err = unix.Fstat(int(f.Fd()), &st); err != nil {
		return err
	}
	if st.Uid != uint32(os.Geteuid()) || st.Mode&0077 != 0 {
		return errors.New("staging directory must be owned by the current account and private (0700)")
	}
	return nil
}

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
func renameExclusive(source *os.File, from string, destination *os.File, to string) error {
	return unix.Renameat2(int(source.Fd()), from, int(destination.Fd()), to, unix.RENAME_NOREPLACE)
}
