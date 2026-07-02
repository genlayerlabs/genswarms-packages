// Package vendorer materializes verified package bytes on disk (design §14.1):
// resolve a swarmidx: ref against the notary, fetch the source, RECOMPUTE the
// dirhash locally and require it to equal the notarized digest — trust the math,
// not the server — then copy the package dir into the vendor root and record it
// in vendor-lock.json. A mismatch writes nothing.
package vendorer

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/genlayerlabs/genswarms-packages/cli/internal/dirhash"
)

// LockEntry records one vendored package.
type LockEntry struct {
	Ref    string `json:"ref"`
	Digest string `json:"digest"`
	Path   string `json:"path"` // relative to the vendor root
}

// Lock is vendor-lock.json: the verified state of the vendor dir. Regenerating
// the dir from the lock re-verifies every digest.
type Lock struct {
	Entries []LockEntry `json:"entries"`
}

// Resolved is what the notary answers for a ref (the subset vendoring needs).
type Resolved struct {
	Digest string
	Source string // github://owner/repo@tag | local:/abs/path
	Dir    string // package dir within the source ("." for the root)
}

var refRe = regexp.MustCompile(`^swarmidx:([^/]+)/([^@]+)@(.+)$`)
var ghRe = regexp.MustCompile(`^github://([^/]+)/([^@]+)@(.+)$`)

// Vendor fetches, verifies and lands one ref. Returns the lock entry.
// resolve is injected (the notary client); fetches go through git or the
// local: scheme (tests).
func Vendor(vendorRoot, ref string, resolve func(string) (Resolved, error)) (LockEntry, error) {
	m := refRe.FindStringSubmatch(ref)
	if m == nil {
		return LockEntry{}, fmt.Errorf("not a swarmidx ref: %q", ref)
	}
	scope, name, version := m[1], m[2], m[3]

	res, err := resolve(ref)
	if err != nil {
		return LockEntry{}, fmt.Errorf("resolve %s: %w", ref, err)
	}
	if res.Digest == "" {
		return LockEntry{}, fmt.Errorf("resolve %s: notary returned no digest", ref)
	}

	rel := fmt.Sprintf("%s__%s@%s", scope, name, version)
	dest := filepath.Join(vendorRoot, rel)

	// Already vendored? Re-hash the on-disk dir (cheap) instead of re-cloning.
	if st, err := os.Stat(dest); err == nil && st.IsDir() {
		if got, err := dirhash.HashDir(dest); err == nil && got == res.Digest {
			return LockEntry{Ref: ref, Digest: res.Digest, Path: rel}, nil
		}
		// Stale or tampered: rebuild from source below.
		if err := os.RemoveAll(dest); err != nil {
			return LockEntry{}, err
		}
	}

	root, cleanup, err := checkout(res.Source)
	if err != nil {
		return LockEntry{}, fmt.Errorf("fetch %s: %w", res.Source, err)
	}
	defer cleanup()

	pkgDir, err := safeJoin(root, res.Dir)
	if err != nil {
		return LockEntry{}, err
	}

	got, err := dirhash.HashDir(pkgDir)
	if err != nil {
		return LockEntry{}, fmt.Errorf("hash %s: %w", ref, err)
	}
	if got != res.Digest {
		return LockEntry{}, fmt.Errorf(
			"digest mismatch for %s: notarized %s, source hashes to %s — refusing to vendor",
			ref, res.Digest, got)
	}

	if err := copyDir(pkgDir, dest); err != nil {
		os.RemoveAll(dest)
		return LockEntry{}, err
	}
	// Paranoia: what we WROTE must hash back too (a copy bug must not
	// silently break the attestation chain).
	if landed, err := dirhash.HashDir(dest); err != nil || landed != res.Digest {
		os.RemoveAll(dest)
		return LockEntry{}, fmt.Errorf("vendored copy of %s does not re-verify", ref)
	}

	return LockEntry{Ref: ref, Digest: res.Digest, Path: rel}, nil
}

// WriteLock persists vendor-lock.json at the vendor root (sorted, stable).
func WriteLock(vendorRoot string, entries []LockEntry) error {
	if err := os.MkdirAll(vendorRoot, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(Lock{Entries: entries}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(vendorRoot, "vendor-lock.json"), append(data, '\n'), 0o644)
}

func checkout(source string) (root string, cleanup func(), err error) {
	if strings.HasPrefix(source, "local:") {
		return strings.TrimPrefix(source, "local:"), func() {}, nil
	}
	if m := ghRe.FindStringSubmatch(source); m != nil {
		owner, repo, tag := m[1], m[2], m[3]
		tmp, err := os.MkdirTemp("", "gsp-vendor-*")
		if err != nil {
			return "", nil, err
		}
		url := fmt.Sprintf("https://github.com/%s/%s.git", owner, repo)
		cmd := exec.Command("git", "clone", "--depth", "1", "--branch", tag, url, tmp)
		if out, err := cmd.CombinedOutput(); err != nil {
			os.RemoveAll(tmp)
			return "", nil, fmt.Errorf("git clone %s@%s: %s", repo, tag, firstLine(out))
		}
		return tmp, func() { os.RemoveAll(tmp) }, nil
	}
	return "", nil, fmt.Errorf("unsupported source scheme: %q", source)
}

func safeJoin(base, rel string) (string, error) {
	if rel == "" || rel == "." {
		return base, nil
	}
	if strings.HasPrefix(rel, "/") || strings.Contains(rel, "..") {
		return "", fmt.Errorf("unsafe package dir: %q", rel)
	}
	return filepath.Join(base, filepath.FromSlash(rel)), nil
}

// copyDir copies files recursively, skipping .git (VCS internals are not
// package content — the dirhash skips them for the same reason).
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), data, 0o644)
	})
}

func firstLine(out []byte) string {
	s := strings.TrimSpace(string(out))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
