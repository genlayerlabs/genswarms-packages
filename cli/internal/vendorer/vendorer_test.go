package vendorer

import (
	"os"
	"path/filepath"
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

	entry, err := Vendor(root, "swarmidx:acme/a@1.0.0", func(string) (Resolved, error) {
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

	_, err := Vendor(root, "swarmidx:acme/a@1.0.0", func(string) (Resolved, error) {
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

func TestVendorReverifiesExistingAndRebuildsTampered(t *testing.T) {
	src, digest := fixtureSource(t)
	root := t.TempDir()
	resolve := func(string) (Resolved, error) {
		return Resolved{Digest: digest, Source: "local:" + src, Dir: "pkgs/a"}, nil
	}

	if _, err := Vendor(root, "swarmidx:acme/a@1.0.0", resolve); err != nil {
		t.Fatal(err)
	}
	// Tamper with the vendored copy; a second run must rebuild it to the digest.
	writeFile(t, filepath.Join(root, "acme__a@1.0.0", "a.ex"), "TAMPERED\n")
	if _, err := Vendor(root, "swarmidx:acme/a@1.0.0", resolve); err != nil {
		t.Fatal(err)
	}
	landed, _ := dirhash.HashDir(filepath.Join(root, "acme__a@1.0.0"))
	if landed != digest {
		t.Fatal("tampered vendor entry was not rebuilt")
	}
}

func TestVendorRejectsUnsafeDir(t *testing.T) {
	src, digest := fixtureSource(t)
	if _, err := Vendor(t.TempDir(), "swarmidx:acme/a@1.0.0", func(string) (Resolved, error) {
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
	entries, err := VendorAll(root, []string{"swarmidx:acme/b@1.0.0"}, resolve)
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
	entries, err := VendorAll(t.TempDir(), []string{"swarmidx:acme/x@1", "swarmidx:acme/y@1"}, resolve)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || calls != 3 {
		t.Fatalf("shared dep must resolve exactly once: entries=%d calls=%d", len(entries), calls)
	}
}
