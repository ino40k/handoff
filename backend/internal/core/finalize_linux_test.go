//go:build linux

package core

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFinalizeUsesVerifiedStageAfterAncestorSwap(t *testing.T) {
	base := t.TempDir()
	dest := filepath.Join(base, "receive")
	stagePath := filepath.Join(dest, ".handoff-partials", "transfer")
	private := filepath.Join(base, "private")
	for _, p := range []string{stagePath, private} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for p, value := range map[string]string{filepath.Join(stagePath, "file"): "verified", filepath.Join(private, "file"): "private"} {
		if err := os.WriteFile(p, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	stage, err := os.OpenRoot(stagePath)
	if err != nil {
		t.Fatal(err)
	}
	defer stage.Close()
	source, err := stage.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	destination, err := os.Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	if err = os.Rename(stagePath, stagePath+"-original"); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(private, stagePath); err != nil {
		t.Fatal(err)
	}
	if err = renameExclusive(source, "file", destination, "file"); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]string{filepath.Join(dest, "file"): "verified", filepath.Join(private, "file"): "private"} {
		got, err := os.ReadFile(p)
		if err != nil || string(got) != want {
			t.Fatalf("wrong object moved: %s: %q %v", filepath.Base(p), got, err)
		}
	}
	if err = os.WriteFile(filepath.Join(stagePath+"-original", "file"), []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = renameExclusive(source, "file", destination, "file"); !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing destination was not protected: %v", err)
	}
}

func TestRejectSharedStagingDirectory(t *testing.T) {
	_, _, dest, _ := setup(t)
	for _, name := range []string{".handoff-partials", ".handoff-partials/" + RandomID()} {
		id := filepath.Base(name)
		if name == ".handoff-partials" {
			id = RandomID()
		}
		p := filepath.Join(dest, name)
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, 0777); err != nil {
			t.Fatal(err)
		}
		stage, err := openStage(&Transfer{ID: id, Destination: dest})
		if err == nil {
			stage.Close()
			t.Fatal("shared staging directory accepted")
		}
		if err = os.Chmod(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
}
