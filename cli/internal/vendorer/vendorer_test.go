package vendorer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/genlayerlabs/genswarms-packages/cli/internal/dirhash"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func vendorFixture(t *testing.T, root, ref string, resolve func(string) (Resolved, error)) (LockEntry, error) {
	t.Helper()
	// All t.TempDir siblings for this test share this private parent.
	return Vendor(root, ref, resolve, Options{LocalSourceRoot: filepath.Dir(t.TempDir())})
}

func fixtureSource(t *testing.T) (src string, digest string) {
	t.Helper()
	src = t.TempDir()
	writeFile(t, filepath.Join(src, "pkgs", "a", "swarm-object.json"),
		`{"module":"Genswarms.A","files":["a_core.ex","a.ex"]}`+"\n")
	writeFile(t, filepath.Join(src, "pkgs", "a", "a_core.ex"), "core\n")
	writeFile(t, filepath.Join(src, "pkgs", "a", "a.ex"), "obj\n")
	d, err := dirhash.HashDir(filepath.Join(src, "pkgs", "a"))
	if err != nil {
		t.Fatal(err)
	}
	return src, d
}

func TestVendorVerifiesAndLands(t *testing.T) {
	src, digest := fixtureSource(t)
	root := t.TempDir()

	entry, err := vendorFixture(t, root, "swarmidx:acme/a@1.0.0", func(string) (Resolved, error) {
		return Resolved{Digest: digest, Source: "local:" + src, Dir: "pkgs/a"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.Path != "acme__a@1.0.0" || entry.Digest != digest {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	landed, err := dirhash.HashDir(filepath.Join(root, entry.Path))
	if err != nil || landed != digest {
		t.Fatalf("vendored dir does not re-verify: %v %s", err, landed)
	}
	if err := WriteLock(root, []LockEntry{entry}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "vendor-lock.json")); err != nil {
		t.Fatal("lock not written")
	}
}

func TestVendorRefusesDigestMismatch(t *testing.T) {
	src, _ := fixtureSource(t)
	root := t.TempDir()

	_, err := vendorFixture(t, root, "swarmidx:acme/a@1.0.0", func(string) (Resolved, error) {
		return Resolved{Digest: "sha256:" + "00", Source: "local:" + src, Dir: "pkgs/a"}, nil
	})
	if err == nil {
		t.Fatal("expected digest-mismatch failure")
	}
	// Nothing landed.
	if _, statErr := os.Stat(filepath.Join(root, "acme__a@1.0.0")); !os.IsNotExist(statErr) {
		t.Fatal("mismatched package was written to the vendor dir")
	}
}

func TestVendorReverifiesExistingAndPreservesTampered(t *testing.T) {
	src, digest := fixtureSource(t)
	root := t.TempDir()
	resolve := func(string) (Resolved, error) {
		return Resolved{Digest: digest, Source: "local:" + src, Dir: "pkgs/a"}, nil
	}

	if _, err := vendorFixture(t, root, "swarmidx:acme/a@1.0.0", resolve); err != nil {
		t.Fatal(err)
	}
	// A second run must preserve operator edits, not silently delete/rebuild them.
	writeFile(t, filepath.Join(root, "acme__a@1.0.0", "a.ex"), "TAMPERED\n")
	if _, err := vendorFixture(t, root, "swarmidx:acme/a@1.0.0", resolve); err == nil {
		t.Fatal("modified existing package accepted")
	}
	landed, _ := dirhash.HashDir(filepath.Join(root, "acme__a@1.0.0"))
	if landed == digest {
		t.Fatal("operator changes were silently rebuilt")
	}
	if data, err := os.ReadFile(filepath.Join(root, "acme__a@1.0.0", "a.ex")); err != nil || string(data) != "TAMPERED\n" {
		t.Fatal("operator changes were not preserved")
	}
}

func TestVendorRejectsUnsafeDir(t *testing.T) {
	src, digest := fixtureSource(t)
	if _, err := vendorFixture(t, t.TempDir(), "swarmidx:acme/a@1.0.0", func(string) (Resolved, error) {
		return Resolved{Digest: digest, Source: "local:" + src, Dir: "../escape"}, nil
	}); err == nil {
		t.Fatal("expected unsafe-dir rejection")
	}
}

func TestVendorAllWalksDepsTransitively(t *testing.T) {
	// b depends on a; vendoring b must land BOTH, each verified.
	srcA, digestA := fixtureSource(t)
	srcB := t.TempDir()
	writeFile(t, filepath.Join(srcB, "pkgs", "b", "swarm-object.json"),
		`{"module":"Genswarms.B"}`+"\n")
	writeFile(t, filepath.Join(srcB, "pkgs", "b", "b.ex"), "obj-b\n")
	digestB, err := dirhash.HashDir(filepath.Join(srcB, "pkgs", "b"))
	if err != nil {
		t.Fatal(err)
	}

	resolve := func(ref string) (Resolved, error) {
		switch ref {
		case "swarmidx:acme/b@1.0.0":
			return Resolved{Digest: digestB, Source: "local:" + srcB, Dir: "pkgs/b",
				Deps: []string{"acme/a@1.0.0"}}, nil
		case "swarmidx:acme/a@1.0.0":
			return Resolved{Digest: digestA, Source: "local:" + srcA, Dir: "pkgs/a"}, nil
		}
		t.Fatalf("unexpected resolve: %s", ref)
		return Resolved{}, nil
	}

	root := t.TempDir()
	entries, err := VendorAll(root, []string{"swarmidx:acme/b@1.0.0"}, resolve, Options{LocalSourceRoot: filepath.Dir(srcA)})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected b + its dep a, got %d entries", len(entries))
	}
	for _, e := range entries {
		if landed, err := dirhash.HashDir(filepath.Join(root, e.Path)); err != nil || landed != e.Digest {
			t.Fatalf("entry %s does not re-verify", e.Ref)
		}
	}
}

func TestVendorAllSharedDepVendoredOnce(t *testing.T) {
	src, digest := fixtureSource(t)
	calls := 0
	resolve := func(ref string) (Resolved, error) {
		calls++
		switch ref {
		case "swarmidx:acme/x@1", "swarmidx:acme/y@1":
			return Resolved{Digest: digest, Source: "local:" + src, Dir: "pkgs/a",
				Deps: []string{"acme/shared@1"}}, nil
		default: // shared
			return Resolved{Digest: digest, Source: "local:" + src, Dir: "pkgs/a"}, nil
		}
	}
	entries, err := VendorAll(t.TempDir(), []string{"swarmidx:acme/x@1", "swarmidx:acme/y@1"}, resolve, Options{LocalSourceRoot: filepath.Dir(src)})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || calls != 3 {
		t.Fatalf("shared dep must resolve exactly once: entries=%d calls=%d", len(entries), calls)
	}
}

func TestFailedReplacementPreservesExistingFiles(t *testing.T) {
	src, digest := fixtureSource(t)
	root := t.TempDir()
	file := filepath.Join(root, "acme__a@1", "a.ex")
	writeFile(t, file, "operator-edited-content\n")
	_, err := vendorFixture(t, root, "swarmidx:acme/a@1", func(string) (Resolved, error) {
		return Resolved{Digest: digest, Source: "local:" + src, Dir: "missing"}, nil
	})
	if err == nil {
		t.Fatal("missing replacement source accepted")
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "operator-edited-content\n" {
		t.Fatalf("failed replacement destroyed existing files: %q %v", data, err)
	}
}

func TestVendorRejectsSourceFileSymlinks(t *testing.T) {
	src := t.TempDir()
	outside := filepath.Join(t.TempDir(), "private-fixture")
	writeFile(t, outside, "private-fixture-bytes\n")
	if err := os.Symlink(outside, filepath.Join(src, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	digest, err := dirhash.HashDir(src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vendorFixture(t, t.TempDir(), "swarmidx:acme/a@1", func(string) (Resolved, error) {
		return Resolved{Digest: digest, Source: "local:" + src, Dir: "."}, nil
	}); err == nil {
		t.Fatal("package read and copied through a source symlink")
	}
}

func TestVendorRejectsDestinationTraversal(t *testing.T) {
	src, digest := fixtureSource(t)
	parent := t.TempDir()
	root := filepath.Join(parent, "vendor")
	if _, err := vendorFixture(t, root, "swarmidx:acme/a@1/../../escaped", func(string) (Resolved, error) {
		return Resolved{Digest: digest, Source: "local:" + src, Dir: "pkgs/a"}, nil
	}); err == nil {
		t.Fatal("path traversal ref accepted")
	}
	if _, err := os.Stat(filepath.Join(parent, "escaped")); !os.IsNotExist(err) {
		t.Fatal("package escaped vendor root")
	}
}

func TestWriteLockDoesNotFollowExistingSymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "private-fixture")
	writeFile(t, outside, "untouched\n")
	if err := os.Symlink(outside, filepath.Join(root, "vendor-lock.json")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := WriteLock(root, nil); err == nil {
		t.Fatal("symlink lock accepted")
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "untouched\n" {
		t.Fatal("lock write changed external file")
	}
}

func TestLocalSourceAuthority(t *testing.T) {
	src, digest := fixtureSource(t)
	for name, opts := range map[string]Options{
		"no authority": {}, "outside approved root": {LocalSourceRoot: t.TempDir()},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Vendor(t.TempDir(), "swarmidx:acme/a@1", func(string) (Resolved, error) {
				return Resolved{Digest: digest, Source: "local:" + src, Dir: "pkgs/a"}, nil
			}, opts)
			if err == nil {
				t.Fatal("local source accepted outside granted authority")
			}
		})
	}
	allowed := t.TempDir()
	link := filepath.Join(allowed, "escape")
	if err := os.Symlink(src, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if root, cleanup, err := checkout("local:"+link, Options{LocalSourceRoot: allowed}); err == nil {
		cleanup()
		t.Fatalf("local source escaped approved directory via symlink: %v", root)
	}
}

func TestPackageDirectoryRejectsSymlinksAndTraversal(t *testing.T) {
	src, digest := fixtureSource(t)
	if err := os.Symlink("pkgs", filepath.Join(src, "alias")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	for _, dir := range []string{"alias/a", "../escape", "/absolute", `pkgs\a`, "C:/outside", ".git", "pkgs/../pkgs/a"} {
		t.Run(dir, func(t *testing.T) {
			_, err := Vendor(t.TempDir(), "swarmidx:acme/a@1", func(string) (Resolved, error) {
				return Resolved{Digest: digest, Source: "local:" + src, Dir: dir}, nil
			}, Options{LocalSourceRoot: src})
			if err == nil {
				t.Fatal("unsafe package directory accepted")
			}
		})
	}
}

func TestExistingDestinationSymlinkIsPreservedAndRejected(t *testing.T) {
	src, digest := fixtureSource(t)
	root := t.TempDir()
	link := filepath.Join(root, "acme__a@1")
	if err := os.Symlink(filepath.Join(src, "pkgs", "a"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err := vendorFixture(t, root, "swarmidx:acme/a@1", func(string) (Resolved, error) {
		return Resolved{Digest: digest, Source: "local:" + src, Dir: "pkgs/a"}, nil
	})
	if err == nil {
		t.Fatal("existing linked destination accepted")
	}
	if st, err := os.Lstat(link); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatal("existing symlink modified")
	}
}

func TestFailedDigestRemovesOnlyPrivateStaging(t *testing.T) {
	src, _ := fixtureSource(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "keep.txt"), "keep\n")
	_, err := vendorFixture(t, root, "swarmidx:acme/a@1", func(string) (Resolved, error) {
		return Resolved{Digest: "sha256:" + strings.Repeat("0", 64), Source: "local:" + src, Dir: "pkgs/a"}, nil
	})
	if err == nil {
		t.Fatal("wrong digest accepted")
	}
	files, err := os.ReadDir(root)
	if err != nil || len(files) != 1 || files[0].Name() != "keep.txt" {
		t.Fatalf("staging leaked or existing entry changed: %v %v", files, err)
	}
}

func TestLockReplacementDoesNotWriteThroughHardlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "private-fixture")
	writeFile(t, outside, "untouched\n")
	if err := os.Link(outside, filepath.Join(root, "vendor-lock.json")); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	if err := WriteLock(root, nil); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "untouched\n" {
		t.Fatal("lock replacement wrote through hardlink")
	}
}

func TestCopyUsesOpenedDirectoryAfterSourcePathIsReplaced(t *testing.T) {
	src, digest := fixtureSource(t)
	root, err := os.OpenRoot(filepath.Join(src, "pkgs", "a"))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.Rename(filepath.Join(src, "pkgs", "a"), filepath.Join(src, "pkgs", "moved")); err != nil {
		t.Skipf("open-directory rename unavailable: %v", err)
	}
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "private-fixture"), "must-not-copy\n")
	if err := os.Symlink(outside, filepath.Join(src, "pkgs", "a")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	dst, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	if err := copyDir(root, dst); err != nil {
		t.Fatal(err)
	}
	if got, err := dirhash.HashFS(dst.FS()); err != nil || got != digest {
		t.Fatalf("copy followed replaced host path: %s %v", got, err)
	}
}
