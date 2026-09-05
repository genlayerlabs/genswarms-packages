// Package vendorer materializes verified package bytes on disk (design §14.1):
// resolve a swarmidx: ref against the notary, fetch the source, RECOMPUTE the
// dirhash locally and require it to equal the notarized digest — trust the math,
// not the server — then copy the package dir into the vendor root and record it
// in vendor-lock.json. A failed package never overwrites an existing package.
package vendorer

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
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
	Source string   // github://owner/repo@tag | local:/abs/path
	Dir    string   // package dir within the source ("." for the root)
	Deps   []string // exact-pin dep refs ("scope/name@version"), walked transitively
}

// Local sources require explicit, independently selected host-path authority.
// A notary signature does not grant permission to read arbitrary client files.
type Options struct{ LocalSourceRoot string }

var refRe = regexp.MustCompile(`^swarmidx:([a-z0-9][a-z0-9-]*)/([a-z0-9][a-z0-9._-]*)@([0-9A-Za-z][0-9A-Za-z.+-]*)$`)
var ghRe = regexp.MustCompile(`^github://([^/]+)/([^@]+)@(.+)$`)

// Vendor fetches, verifies and lands one ref. Returns the lock entry.
// resolve is injected (the notary client); fetches go through git or the
// local: scheme (tests).
func Vendor(vendorRoot, ref string, resolve func(string) (Resolved, error), opts Options) (LockEntry, error) {
	res, err := resolve(ref)
	if err != nil {
		return LockEntry{}, fmt.Errorf("resolve %s: %w", ref, err)
	}
	return vendorResolved(vendorRoot, ref, res, opts)
}

// vendorResolved verifies and lands one already-resolved ref.
func vendorResolved(vendorRoot, ref string, res Resolved, opts Options) (LockEntry, error) {
	m := refRe.FindStringSubmatch(ref)
	if m == nil {
		return LockEntry{}, fmt.Errorf("not a swarmidx ref: %q", ref)
	}
	scope, name, version := m[1], m[2], m[3]

	if res.Digest == "" {
		return LockEntry{}, fmt.Errorf("resolve %s: notary returned no digest", ref)
	}

	rel := fmt.Sprintf("%s__%s@%s", scope, name, version)
	dest, err := openVendorRoot(vendorRoot)
	if err != nil {
		return LockEntry{}, err
	}
	defer dest.Close()

	// Already vendored? Re-hash the on-disk dir (cheap) instead of re-cloning.
	if st, err := dest.Lstat(rel); err == nil {
		if !st.IsDir() {
			return LockEntry{}, fmt.Errorf("existing package is not a regular directory: %s", rel)
		}
		existing, err := dest.OpenRoot(rel)
		if err != nil {
			return LockEntry{}, err
		}
		defer existing.Close()
		if err := regularTree(existing); err != nil {
			return LockEntry{}, err
		}
		if got, err := dirhash.HashFS(existing.FS()); err == nil && got == res.Digest {
			return LockEntry{Ref: ref, Digest: res.Digest, Path: rel}, nil
		}
		return LockEntry{}, fmt.Errorf("existing package differs from signed digest; preserved without changes: %s", rel)
	} else if !os.IsNotExist(err) {
		return LockEntry{}, err
	}

	root, cleanup, err := checkout(res.Source, opts)
	if err != nil {
		return LockEntry{}, fmt.Errorf("fetch %s: %w", res.Source, err)
	}
	defer cleanup()

	pkgDir, err := packageRoot(root, res.Dir)
	if err != nil {
		return LockEntry{}, err
	}
	defer pkgDir.Close()

	stageName := ".gsp-stage-" + rand.Text()
	if err := dest.Mkdir(stageName, 0o700); err != nil {
		return LockEntry{}, err
	}
	defer dest.RemoveAll(stageName) // Only this call's private staging directory.
	stage, err := dest.OpenRoot(stageName)
	if err != nil {
		return LockEntry{}, err
	}
	if err := copyDir(pkgDir, stage); err != nil {
		stage.Close()
		return LockEntry{}, err
	}
	got, hashErr := dirhash.HashFS(stage.FS())
	if hashErr == nil && got == res.Digest {
		hashErr = stage.Chmod(".", 0o755)
	}
	stage.Close()
	if hashErr != nil {
		return LockEntry{}, fmt.Errorf("hash %s: %w", ref, hashErr)
	}
	if got != res.Digest {
		return LockEntry{}, fmt.Errorf("digest mismatch for %s; refusing to vendor", ref)
	}
	if _, err := dest.Lstat(rel); !os.IsNotExist(err) {
		return LockEntry{}, fmt.Errorf("package destination appeared during verification: %s", rel)
	}
	if err := dest.Rename(stageName, rel); err != nil {
		return LockEntry{}, err
	}

	return LockEntry{Ref: ref, Digest: res.Digest, Path: rel}, nil
}

// WriteLock persists vendor-lock.json at the vendor root (sorted, stable).
func WriteLock(vendorRoot string, entries []LockEntry) error {
	root, err := openVendorRoot(vendorRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	if st, err := root.Lstat("vendor-lock.json"); err == nil && !st.Mode().IsRegular() {
		return fmt.Errorf("vendor lock is not a regular file; preserved without changes")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	entries = append([]LockEntry{}, entries...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Ref < entries[j].Ref })
	data, err := json.MarshalIndent(Lock{Entries: entries}, "", "  ")
	if err != nil {
		return err
	}
	stage := ".gsp-lock-" + rand.Text()
	f, err := root.OpenFile(stage, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer root.Remove(stage)
	_, err = f.Write(append(data, '\n'))
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	// Rename replaces the directory entry, never writes through a link/hardlink.
	return root.Rename(stage, "vendor-lock.json")
}

func checkout(source string, opts Options) (root *os.Root, cleanup func(), err error) {
	if strings.HasPrefix(source, "local:") {
		if opts.LocalSourceRoot == "" {
			return nil, nil, fmt.Errorf("local source requires --local-source-root")
		}
		path := strings.TrimPrefix(source, "local:")
		if !filepath.IsAbs(path) {
			return nil, nil, fmt.Errorf("local source must be absolute")
		}
		allowed, err := filepath.Abs(opts.LocalSourceRoot)
		if err != nil {
			return nil, nil, err
		}
		rel, err := filepath.Rel(allowed, path)
		if err != nil || !filepath.IsLocal(rel) {
			return nil, nil, fmt.Errorf("local source is outside approved root")
		}
		base, err := os.OpenRoot(allowed)
		if err != nil {
			return nil, nil, err
		}
		defer base.Close()
		root, err := base.OpenRoot(rel)
		if err != nil {
			return nil, nil, err
		}
		return root, func() { root.Close() }, nil
	}
	if m := ghRe.FindStringSubmatch(source); m != nil {
		owner, repo, tag := m[1], m[2], m[3]
		tmp, err := os.MkdirTemp("", "gsp-vendor-*")
		if err != nil {
			return nil, nil, err
		}
		url := fmt.Sprintf("https://github.com/%s/%s.git", owner, repo)
		cmd := exec.Command("git", "clone", "--depth", "1", "--branch", tag, url, tmp)
		if out, err := cmd.CombinedOutput(); err != nil {
			os.RemoveAll(tmp)
			return nil, nil, fmt.Errorf("git clone %s@%s: %s", repo, tag, firstLine(out))
		}
		root, err := os.OpenRoot(tmp)
		if err != nil {
			os.RemoveAll(tmp)
			return nil, nil, err
		}
		return root, func() { root.Close(); os.RemoveAll(tmp) }, nil
	}
	return nil, nil, fmt.Errorf("unsupported source scheme: %q", source)
}

func packageRoot(base *os.Root, rel string) (*os.Root, error) {
	if !fs.ValidPath(rel) || strings.ContainsAny(rel, `\:`) {
		return nil, fmt.Errorf("unsafe package dir")
	}
	part := ""
	for _, name := range strings.Split(rel, "/") {
		if name == ".git" {
			return nil, fmt.Errorf("package dir cannot be VCS internals")
		}
		part = filepath.Join(part, name)
		st, err := base.Lstat(part)
		if err != nil {
			return nil, err
		}
		if !st.IsDir() {
			return nil, fmt.Errorf("package dir contains a non-directory or symlink")
		}
	}
	return base.OpenRoot(filepath.FromSlash(rel))
}

// copyDir copies files recursively, skipping .git (VCS internals are not
// package content — the dirhash skips them for the same reason).
func copyDir(src, dst *os.Root) error {
	return fs.WalkDir(src.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return dst.MkdirAll(filepath.FromSlash(p), 0o755)
		}
		// DirEntry.Info can reopen its display path after the directory moves.
		// Keep metadata reads anchored to the same root as content reads.
		info, err := src.Lstat(filepath.FromSlash(p))
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("package contains symlink or special file: %s", p)
		}
		in, err := src.Open(filepath.FromSlash(p))
		if err != nil {
			return err
		}
		defer in.Close()
		info, err = in.Stat()
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("package file changed type during copy")
		}
		out, err := dst.OpenFile(filepath.FromSlash(p), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, in)
		closeErr := out.Close()
		if err != nil {
			return err
		}
		return closeErr
	})
}

func regularTree(root *os.Root) error {
	return fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		info, err := root.Lstat(filepath.FromSlash(p))
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("package contains symlink or special file: %s", p)
		}
		return nil
	})
}

func openVendorRoot(path string) (*os.Root, error) {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return nil, err
	}
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("vendor root must be a directory, not a symlink")
	}
	return os.OpenRoot(path)
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

// VendorAll vendors refs AND their notarized deps, breadth-first: each
// resolved release's exact-pin deps ("scope/name@version") join the queue as
// swarmidx: refs. The registry guarantees the dep graph is a DAG (a dep must
// be notarized before its dependent), so the seen-set is enough; the depth cap
// is a corrupted-registry backstop, not a design limit.
func VendorAll(vendorRoot string, refs []string, resolve func(string) (Resolved, error), opts Options) ([]LockEntry, error) {
	const maxDepth = 32

	seen := map[string]bool{}
	queue := append([]string{}, refs...)
	var entries []LockEntry

	for depth := 0; len(queue) > 0; depth++ {
		if depth > maxDepth {
			return nil, fmt.Errorf("dependency chain deeper than %d — refusing (corrupted registry?)", maxDepth)
		}
		var next []string
		for _, ref := range queue {
			if seen[ref] {
				continue
			}
			seen[ref] = true

			res, err := resolve(ref)
			if err != nil {
				return nil, fmt.Errorf("resolve %s: %w", ref, err)
			}
			entry, err := vendorResolved(vendorRoot, ref, res, opts)
			if err != nil {
				return nil, err
			}
			entries = append(entries, entry)
			for _, dep := range res.Deps {
				next = append(next, "swarmidx:"+dep)
			}
		}
		queue = next
	}
	return entries, nil
}
