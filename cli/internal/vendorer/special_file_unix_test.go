//go:build unix

package vendorer

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestRejectsFIFOWithoutOpeningIt(t *testing.T) {
	path := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(path, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := regularTree(root); err == nil {
		t.Fatal("FIFO accepted as existing package content")
	}
	dst, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	if err := copyDir(root, dst); err == nil {
		t.Fatal("FIFO copied")
	}
}
