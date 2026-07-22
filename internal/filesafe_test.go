package internal

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSafeDeleteMediaFile(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "TV", "Show", "Season 01")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(dir, "Show.S01E01.mkv")
	if err := os.WriteFile(f, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := safeDeleteMediaFile(f, root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Fatal("expected file removed")
	}

	escape := filepath.Join(t.TempDir(), "outside.mkv")
	if err := os.WriteFile(escape, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	_ = safeDeleteMediaFile(escape, root)
	if _, err := os.Stat(escape); err != nil {
		t.Fatal("path outside root must not be deleted")
	}
}
