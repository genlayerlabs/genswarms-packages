package dirhash

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func writeTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "b.txt"), []byte("world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestHashDirFormat(t *testing.T) {
	dir := writeTree(t)
	h, err := HashDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(h) {
		t.Fatalf("digest %q does not match the ref digest contract", h)
	}
}

func TestHashDirDeterministic(t *testing.T) {
	dir := writeTree(t)
	h1, err := HashDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Re-hash the same bytes (a fresh identical tree) → identical digest,
	// independent of the temp path.
	dir2 := writeTree(t)
	h2, err := HashDir(dir2)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("non-reproducible: %s != %s", h1, h2)
	}
}

func TestHashDirSensitive(t *testing.T) {
	dir := writeTree(t)
	before, _ := HashDir(dir)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	after, _ := HashDir(dir)
	if before == after {
		t.Fatal("digest did not change after a content change")
	}
}
