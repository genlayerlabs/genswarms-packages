package vendorer

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestBatchFailureLandsNoPackagesAndPreservesLock(t *testing.T) {
	source, digest := fixtureSource(t)
	root := t.TempDir()
	old := "{\"entries\":[]}\n"
	writeFile(t, filepath.Join(root, "vendor-lock.json"), old)
	_, err := VendorAll(root, []string{"swarmidx:acme/a@1"}, func(ref string) (Resolved, error) {
		if ref == "swarmidx:acme/missing@1" {
			return Resolved{}, fmt.Errorf("fixture missing")
		}
		return Resolved{Digest: digest, Source: "local:" + source, Dir: "pkgs/a", Deps: []string{"acme/missing@1"}}, nil
	}, Options{LocalSourceRoot: filepath.Dir(source)})
	if err == nil {
		t.Fatal("accepted missing dependency")
	}
	if _, err := os.Stat(filepath.Join(root, "acme__a@1")); !os.IsNotExist(err) {
		t.Fatal("earlier package remained after failed batch")
	}
	bytes, _ := os.ReadFile(filepath.Join(root, "vendor-lock.json"))
	if string(bytes) != old {
		t.Fatal("failed batch modified old lock")
	}
	if _, err := os.Stat(filepath.Join(root, pendingDir)); !os.IsNotExist(err) {
		t.Fatal("failed preparation retained lease")
	}
}

func TestConcurrentVendorWriterIsRefused(t *testing.T) {
	source, digest := fixtureSource(t)
	root := t.TempDir()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	resolve := func(string) (Resolved, error) {
		close(entered)
		<-release
		return Resolved{Digest: digest, Source: "local:" + source, Dir: "pkgs/a"}, nil
	}
	go func() {
		_, err := VendorAll(root, []string{"swarmidx:acme/a@1"}, resolve, Options{LocalSourceRoot: filepath.Dir(source)})
		done <- err
	}()
	<-entered
	_, err := VendorAll(root, nil, resolve, Options{})
	if err == nil {
		t.Error("second writer accepted")
	}
	if err := Recover(root); err == nil {
		t.Error("recovered a live writer")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryAfterWriterProcessExit(t *testing.T) {
	if os.Getenv("GSP_TEST_CRASH_WRITER") != "" {
		root, err := openVendorRoot(os.Getenv("GSP_TEST_ROOT"))
		if err != nil {
			panic(err)
		}
		if err := root.Mkdir(pendingDir, 0o700); err != nil {
			panic(err)
		}
		entry, err := vendorResolved(os.Getenv("GSP_TEST_ROOT"), "swarmidx:acme/a@1", Resolved{Digest: os.Getenv("GSP_TEST_DIGEST"), Source: "local:" + os.Getenv("GSP_TEST_SOURCE"), Dir: "pkgs/a"}, Options{LocalSourceRoot: filepath.Dir(os.Getenv("GSP_TEST_SOURCE"))})
		if err != nil {
			panic(err)
		}
		if err := writeJournal(root, journal{PID: os.Getpid(), New: []LockEntry{entry}}); err != nil {
			panic(err)
		}
		if os.Getenv("GSP_TEST_COMMITTED") == "1" {
			if err := WriteLock(os.Getenv("GSP_TEST_ROOT"), []LockEntry{entry}); err != nil {
				panic(err)
			}
		}
		os.Exit(0) // No lease cleanup, either before or after the lock commit.
	}
	for _, scenario := range []struct{ changed, committed bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		t.Run(fmt.Sprintf("changed=%v/committed=%v", scenario.changed, scenario.committed), func(t *testing.T) {
			source, digest := fixtureSource(t)
			root := t.TempDir()
			cmd := exec.Command(os.Args[0], "-test.run=^TestRecoveryAfterWriterProcessExit$")
			cmd.Env = append(os.Environ(), "GSP_TEST_CRASH_WRITER=1", "GSP_TEST_ROOT="+root, "GSP_TEST_SOURCE="+source, "GSP_TEST_DIGEST="+digest)
			if scenario.committed {
				cmd.Env = append(cmd.Env, "GSP_TEST_COMMITTED=1")
			}
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("fixture failed: %v %s", err, out)
			}
			file := filepath.Join(root, "acme__a@1", "a.ex")
			if scenario.changed {
				writeFile(t, file, "operator edit")
			}
			err := Recover(root)
			if scenario.committed {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(file); err != nil {
					t.Fatal("committed package was removed", err)
				}
				if scenario.changed {
					bytes, _ := os.ReadFile(file)
					if string(bytes) != "operator edit" {
						t.Fatal("committed edit lost")
					}
				}
			} else if scenario.changed {
				if err == nil {
					t.Fatal("recovery erased changed package")
				}
				bytes, _ := os.ReadFile(file)
				if string(bytes) != "operator edit" {
					t.Fatal("edit lost")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(filepath.Join(root, "acme__a@1")); !os.IsNotExist(err) {
					t.Fatal("uncommitted entry retained")
				}
			}
		})
	}
}
