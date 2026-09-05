package vendorer

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/genlayerlabs/genswarms-packages/cli/internal/dirhash"
)

const pendingDir = ".gsp-installing"

type journal struct {
	PID int         `json:"pid"`
	New []LockEntry `json:"new"`
}

// One directory lease owns an entire dependency batch, including its lock file.
// All bytes are staged before publication. A crashed lease is refused until
// Recover verifies its journal and the bytes of any partially installed entries.
func installBatch(path string, refs []string, resolve func(string) (Resolved, error), opts Options) (entries []LockEntry, err error) {
	root, err := openVendorRoot(path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err = root.Mkdir(pendingDir, 0o700); err != nil {
		return nil, fmt.Errorf("vendor writer active or interrupted; inspect/recover the pending installation")
	}
	retainJournal := false
	defer func() {
		if !retainJournal {
			root.RemoveAll(pendingDir)
		}
	}()
	old, err := readLock(root)
	if err != nil {
		return nil, err
	}
	j := journal{PID: os.Getpid()}
	if err = writeJournal(root, j); err != nil {
		return nil, err
	}
	stagePath := filepath.Join(path, pendingDir)
	entries, err = vendorGraph(refs, resolve, func(ref string, res Resolved) (LockEntry, error) {
		rel, e := refPath(ref)
		if e != nil {
			return LockEntry{}, e
		}
		if _, e := root.Lstat(rel); e == nil {
			return vendorResolved(path, ref, res, opts)
		} else if !os.IsNotExist(e) {
			return LockEntry{}, e
		}
		entry, e := vendorResolved(stagePath, ref, res, opts)
		if e == nil {
			j.New = append(j.New, entry)
		}
		return entry, e
	})
	if err != nil {
		return nil, err
	}
	merged := map[string]LockEntry{}
	for _, entry := range old {
		merged[entry.Ref] = entry
	}
	for _, entry := range entries {
		if previous, ok := merged[entry.Ref]; ok && previous != entry {
			return nil, fmt.Errorf("existing lock conflicts with resolved package")
		}
		merged[entry.Ref] = entry
	}
	if err = writeJournal(root, j); err != nil {
		return nil, err
	}
	var moved []LockEntry
	for _, entry := range j.New {
		if _, e := root.Lstat(entry.Path); !os.IsNotExist(e) {
			err = fmt.Errorf("package destination appeared during commit")
			break
		}
		if err = root.Rename(filepath.Join(pendingDir, entry.Path), entry.Path); err != nil {
			break
		}
		moved = append(moved, entry)
	}
	if err == nil {
		all := make([]LockEntry, 0, len(merged))
		for _, entry := range merged {
			all = append(all, entry)
		}
		err = WriteLock(path, all)
	}
	if err != nil {
		if recoveryErr := rollback(root, moved); recoveryErr != nil {
			retainJournal = true
			return nil, fmt.Errorf("installation failed; changed package bytes preserved: %w", recoveryErr)
		}
		return nil, err
	}
	return entries, nil
}

func refPath(ref string) (string, error) {
	m := refRe.FindStringSubmatch(ref)
	if m == nil {
		return "", fmt.Errorf("invalid package reference")
	}
	return fmt.Sprintf("%s__%s@%s", m[1], m[2], m[3]), nil
}

func readLock(root *os.Root) ([]LockEntry, error) {
	st, err := root.Lstat("vendor-lock.json")
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("vendor lock is not a regular file")
	}
	bytes, err := root.ReadFile("vendor-lock.json")
	if err != nil {
		return nil, err
	}
	var lock Lock
	if err = json.Unmarshal(bytes, &lock); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, entry := range lock.Entries {
		rel, err := refPath(entry.Ref)
		if err != nil || rel != entry.Path || seen[entry.Ref] {
			return nil, fmt.Errorf("invalid vendor lock entry")
		}
		seen[entry.Ref] = true
	}
	return lock.Entries, nil
}

func writeJournal(root *os.Root, j journal) error {
	bytes, err := json.Marshal(j)
	if err != nil {
		return err
	}
	file, err := root.OpenFile(filepath.Join(pendingDir, "journal.tmp"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = file.Write(bytes)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return root.Rename(filepath.Join(pendingDir, "journal.tmp"), filepath.Join(pendingDir, "journal.json"))
}

func rollback(root *os.Root, entries []LockEntry) error {
	// Verify every candidate before removing any, and never remove old entries.
	var present []string
	for _, entry := range entries {
		rel, err := refPath(entry.Ref)
		if err != nil || rel != entry.Path {
			return fmt.Errorf("invalid recovery path")
		}
		if st, err := root.Lstat(rel); os.IsNotExist(err) {
			continue
		} else if err != nil || !st.IsDir() {
			return fmt.Errorf("recovery destination changed")
		}
		pkg, err := root.OpenRoot(rel)
		if err != nil {
			return err
		}
		err = regularTree(pkg)
		digest, hashErr := dirhash.HashFS(pkg.FS())
		pkg.Close()
		if err != nil || hashErr != nil || digest != entry.Digest {
			return fmt.Errorf("recovery package has changed; preserved")
		}
		present = append(present, rel)
	}
	for _, rel := range present {
		if err := root.RemoveAll(rel); err != nil {
			return err
		}
	}
	return nil
}

// Recover is explicit and refuses a live/unknown writer. It preserves committed
// entries and rolls back only verified, previously-new entries of a dead writer.
func Recover(path string) error {
	root, err := openVendorRoot(path)
	if err != nil {
		return err
	}
	defer root.Close()
	st, err := root.Lstat(pendingDir)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("invalid pending installation")
	}
	pending, err := root.OpenRoot(pendingDir)
	if err != nil {
		return err
	}
	defer pending.Close()
	if err = pending.Mkdir("recovering", 0o700); err != nil {
		return fmt.Errorf("another recovery is active")
	}
	defer pending.Remove("recovering")
	journalInfo, err := pending.Lstat("journal.json")
	if err != nil || !journalInfo.Mode().IsRegular() {
		return fmt.Errorf("recovery journal is not a regular file")
	}
	bytes, err := pending.ReadFile("journal.json")
	if err != nil {
		return err
	}
	var j journal
	if err = json.Unmarshal(bytes, &j); err != nil || j.PID <= 0 {
		return fmt.Errorf("invalid recovery journal")
	}
	process, err := os.FindProcess(j.PID)
	if err == nil {
		err = process.Signal(syscall.Signal(0))
		process.Release()
	}
	if !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("writer is alive or cannot be checked; recovery refused")
	}
	locked, err := readLock(root)
	if err != nil {
		return err
	}
	committed := map[LockEntry]bool{}
	for _, entry := range locked {
		committed[entry] = true
	}
	var remove []LockEntry
	for _, entry := range j.New {
		if !committed[entry] {
			remove = append(remove, entry)
		}
	}
	if err = rollback(root, remove); err != nil {
		return err
	}
	return root.RemoveAll(pendingDir)
}
