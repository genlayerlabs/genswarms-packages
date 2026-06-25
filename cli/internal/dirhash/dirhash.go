// Package dirhash computes a reproducible digest over a directory (design §10).
//
// It reuses Go modules' dirhash.Hash1 — the sorted list of `sha256(bytes)  path`
// per file, hashed — so the algorithm is the one the whole notary design mirrors
// (proxy.golang.org / sum.golang.org). Hash1 emits "h1:<base64>"; we re-emit the
// same 32-byte SHA-256 as "sha256:<hex>" so the digest conforms to the genswarms
// ref digest contract (`^[a-z0-9]+:[0-9a-f]+$`).
package dirhash

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/mod/sumdb/dirhash"
)

// HashDir returns the reproducible digest of dir as "sha256:<hex>". Files are
// named by their slash-separated path relative to dir, independent of git, tar,
// mtime, ordering or permissions.
func HashDir(dir string) (string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return "", err
	}

	open := func(name string) (io.ReadCloser, error) {
		return os.Open(filepath.Join(dir, filepath.FromSlash(name)))
	}
	h1, err := dirhash.Hash1(files, open)
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(h1, "h1:"))
	if err != nil {
		return "", fmt.Errorf("decode dirhash: %w", err)
	}
	return "sha256:" + hex.EncodeToString(raw), nil
}
