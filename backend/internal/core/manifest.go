package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func parentPath(p string) string {
	d := path.Dir(p)
	if d == "." {
		return ""
	}
	return d
}
func ValidatePath(p string) (string, error) {
	if len(p) == 0 || len(p) > 4096 || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\:\x00") || path.Clean(p) != p {
		return "", errors.New("unsafe relative path")
	}
	for _, c := range strings.Split(p, "/") {
		if c == "." || c == ".." || len(c) > 240 || strings.TrimRight(c, ". ") != c || strings.ContainsAny(c, "<>\"|?*") {
			return "", errors.New("unsupported filename")
		}
		for _, r := range c {
			if r < 32 {
				return "", errors.New("control character in filename")
			}
		}
		stem := strings.ToUpper(strings.SplitN(c, ".", 2)[0])
		if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" || stem == "CONIN$" || stem == "CONOUT$" || (len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '0' && stem[3] <= '9') {
			return "", errors.New("reserved Windows filename")
		}
		if strings.EqualFold(c, ".handoff-partials") {
			return "", errors.New("reserved staging name")
		}
	}
	return strings.ToLower(p), nil
}
func BuildManifest(ctx context.Context, paths []string) (Manifest, error) {
	m := Manifest{ID: RandomID(), Entries: []Entry{}}
	for _, src := range paths {
		abs, err := filepath.Abs(src)
		if err != nil {
			return m, err
		}
		top := filepath.Base(abs)
		if _, err = ValidatePath(top); err != nil {
			return m, err
		}
		// Anchor traversal to a root and reject links, devices, sockets and FIFOs.
		root, err := os.OpenRoot(filepath.Dir(abs))
		if err != nil {
			return m, err
		}
		err = walk(ctx, root, top, top, &m)
		root.Close()
		if err != nil {
			return m, err
		}
	}
	m.Digest = m.CalculateDigest()
	if err := ValidateManifest(m.Public()); err != nil {
		return m, err
	}
	b, _ := json.Marshal(m.Public())
	if len(b) > MaxManifest {
		return m, errors.New("manifest exceeds 32 MiB limit")
	}
	return m, nil
}
func walk(ctx context.Context, root *os.Root, rel, out string, m *Manifest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(m.Entries) >= MaxEntries {
		return errors.New("too many entries")
	}
	st, err := root.Lstat(rel)
	if err != nil {
		return err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("symbolic links are not transferred: %s", out)
	}
	if st.IsDir() {
		m.Entries = append(m.Entries, Entry{Path: out, Dir: true})
		d, err := root.Open(rel)
		if err != nil {
			return err
		}
		entries, err := d.ReadDir(-1)
		d.Close()
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err = walk(ctx, root, path.Join(rel, e.Name()), path.Join(out, e.Name()), m); err != nil {
				return err
			}
		}
		return nil
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("not a regular file: %s", out)
	}
	f, err := root.Open(rel)
	if err != nil {
		return err
	}
	defer f.Close()
	now, err := f.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(st, now) {
		return errors.New("source changed during scan")
	}
	ent := Entry{Path: out, Size: st.Size(), Source: filepath.Join(root.Name(), filepath.FromSlash(rel)), Chunks: []string{}}
	h := sha256.New()
	buf := make([]byte, ChunkSize)
	var read int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := io.ReadFull(f, buf)
		if n > 0 {
			h.Write(buf[:n])
			ch := sha256.Sum256(buf[:n])
			ent.Chunks = append(ent.Chunks, hex.EncodeToString(ch[:]))
			read += int64(n)
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return err
		}
	}
	after, err := f.Stat()
	if err != nil {
		return err
	}
	if read != ent.Size || after.Size() != st.Size() || !after.ModTime().Equal(st.ModTime()) {
		return errors.New("source changed while hashing")
	}
	ent.Hash = hex.EncodeToString(h.Sum(nil))
	m.Total += ent.Size
	m.Entries = append(m.Entries, ent)
	return nil
}
func ReadChunk(e Entry, offset int64) ([]byte, error) {
	if offset < 0 || offset >= e.Size || offset%ChunkSize != 0 {
		return nil, errors.New("invalid offset")
	}
	root, err := os.OpenRoot(filepath.Dir(e.Source))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	name := filepath.Base(e.Source)
	st, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, errors.New("source replaced with non-file")
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fs, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(st, fs) || fs.Size() != e.Size {
		return nil, errors.New("source changed")
	}
	n := min(int64(ChunkSize), e.Size-offset)
	buf := make([]byte, n)
	if _, err = f.ReadAt(buf, offset); err != nil {
		return nil, err
	}
	h := sha256.Sum256(buf)
	if hex.EncodeToString(h[:]) != e.Chunks[offset/ChunkSize] {
		return nil, errors.New("source contents changed; create a new transfer")
	}
	return buf, nil
}
