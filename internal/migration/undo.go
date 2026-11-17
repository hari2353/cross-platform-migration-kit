package migration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Steal: PCmover Professional's paid-tier differentiator is Undo. The
// restore plan already enumerates every target; the undo journal is that
// plan's PRE-state, captured before the copy loop runs and stored
// encrypted in the repository — the same streaming object format as every
// captured file. Only files the restore actually copied are reverted —
// skipped targets are untouched. Honest limitation, stated in every undo
// report: applications and settings are not reverted.
//
// The journal is keyed BY DESTINATION (sha-256 of the destination path):
// one bundle restored to two machines keeps a journal — and an undo — for
// each. A journal from an interrupted pass survives resume; it is consumed
// on success, so a second undo fails loudly.

type undoEntry struct {
	ID        string `json:"id"`
	Target    string `json:"target"`
	Pre       string `json:"pre"` // "absent" | "present"
	PreSHA256 string `json:"pre_sha256,omitempty"`
	PreSize   int64  `json:"pre_size,omitempty"`
	PreMode   uint32 `json:"pre_mode,omitempty"`
}

type undoJournal struct {
	Version     int         `json:"version"`
	Destination string      `json:"destination"`
	Entries     []undoEntry `json:"entries"`
}

func undoJournalPath(repository, destination string) string {
	digest := sha256.Sum256([]byte(destination))
	return filepath.Join(repository,
		fmt.Sprintf("undo-journal-%s.enc", hex.EncodeToString(digest[:8])))
}

func undoObjectPath(repository, id string) string {
	return filepath.Join(repository, objectDir, "undo-"+id+".bin")
}

func writeUndoJournal(repository string, key []byte, destination string, plans []CopyPlan) error {
	journalPath := undoJournalPath(repository, destination)
	if _, err := os.Stat(journalPath); err == nil {
		// A journal from an earlier (possibly interrupted) pass stands:
		// the pre-state it recorded is the truth undo must revert to.
		return nil
	}
	journal := undoJournal{Version: 1, Destination: destination}
	for _, plan := range plans {
		entry := undoEntry{ID: plan.Item.ID, Target: plan.Target, Pre: "absent"}
		if info, statErr := os.Stat(plan.Target); statErr == nil && info.Mode().IsRegular() {
			// pre-state digest (streamed) and streamed backup through the
			// same chunked object path capture uses — a 20GB pre-existing
			// target never sits whole in RAM
			digest, hashErr := fileSHA256(plan.Target)
			if hashErr != nil {
				return fmt.Errorf("hash pre-existing %s: %w", plan.Item.RelativePath, hashErr)
			}
			_ = os.Remove(undoObjectPath(repository, plan.Item.ID)) // stale object from a crashed pass
			if encErr := encryptObjectFunc(plan.Target, undoObjectPath(repository, plan.Item.ID), key, "undo-"+plan.Item.ID); encErr != nil {
				return fmt.Errorf("back up pre-existing %s: %w", plan.Item.RelativePath, encErr)
			}
			entry.Pre = "present"
			entry.PreSHA256 = digest
			entry.PreSize = info.Size()
			entry.PreMode = uint32(info.Mode().Perm())
		}
		journal.Entries = append(journal.Entries, entry)
	}
	encoded, err := json.Marshal(journal)
	if err != nil {
		return fmt.Errorf("encode undo journal: %w", err)
	}
	ciphertext, err := encryptBytes(key, encoded, []byte("undo-journal-v1"))
	if err != nil {
		return fmt.Errorf("encrypt undo journal: %w", err)
	}
	// atomic write (temp + rename), mirroring the restore-state pattern —
	// a crash mid-write must never leave a half-journal that reads as valid
	temp, tempErr := osCreateTemp(repository, ".undo-journal-*.tmp")
	if tempErr != nil {
		return tempErr
	}
	temporaryPath := temp.Name()
	discard := func() { _ = removePath(temporaryPath) }
	if _, writeErr := fileWrite(temp, ciphertext); writeErr != nil {
		_ = fileClose(temp)
		discard()
		return writeErr
	}
	if closeErr := fileClose(temp); closeErr != nil {
		discard()
		return closeErr
	}
	if err := os.Rename(temporaryPath, journalPath); err != nil {
		discard()
		return err
	}
	return nil
}

func loadUndoJournal(repository string, key []byte, destination string) (undoJournal, error) {
	ciphertext, err := os.ReadFile(undoJournalPath(repository, destination))
	if err != nil {
		return undoJournal{}, errors.New("nothing to undo for this destination: no restore journal in the repository")
	}
	plaintext, err := decryptBytes(key, ciphertext, []byte("undo-journal-v1"))
	if err != nil {
		return undoJournal{}, fmt.Errorf("decrypt undo journal: %w", err)
	}
	var journal undoJournal
	if err := json.Unmarshal(plaintext, &journal); err != nil {
		return undoJournal{}, fmt.Errorf("decode undo journal: %w", err)
	}
	if destination != "" && journal.Destination != destination {
		return undoJournal{}, fmt.Errorf("journal was recorded for destination %s, not %s",
			journal.Destination, destination)
	}
	return journal, nil
}

// UndoRestore reverts the effects of the last managed restore to THIS
// destination: files the restore copied are removed (absent pre-state) or
// replaced with their original bytes (present pre-state, streamed back and
// verified against the recorded SHA-256). Targets that already vanished
// are reported "missing", never as fake successes. The journal is consumed
// on success; a second undo fails loudly. Applications and settings are
// NOT reverted — the report says so.
func UndoRestore(repository, destination, key string) (Report, error) {
	keyBytes, err := ReadKey(key)
	if err != nil {
		return Report{}, err
	}
	journal, err := loadUndoJournal(repository, keyBytes, destination)
	if err != nil {
		return Report{}, err
	}
	state, err := LoadRestoreState(repository, keyBytes)
	if err != nil {
		return Report{}, fmt.Errorf("load restore state: %w", err)
	}
	report := NewReport("undo")
	report.Destination = journal.Destination
	var removedTargets []string
	undone := 0
	for _, entry := range journal.Entries {
		current, ok := state.Items[entry.ID]
		if !ok || current.Status != "copied" || current.UndoneIn == journal.Destination {
			// journal consumed below — its backup objects are useless now
			_ = os.Remove(undoObjectPath(repository, entry.ID))
			continue // the restore never changed this target here
		}
		undone++
		state.Items[entry.ID] = ItemState{Status: "copied", UndoneIn: journal.Destination}
		switch entry.Pre {
		case "absent":
			if _, statErr := os.Stat(entry.Target); statErr != nil && os.IsNotExist(statErr) {
				// the restored file is already gone (destination wiped or
				// unmounted) — say so, never claim a removal that happened
				report.Add(Result{Kind: "file", ID: entry.ID, Path: entry.Target, Status: "missing"})
				continue
			}
			if err := os.Remove(entry.Target); err != nil && !os.IsNotExist(err) {
				return report, fmt.Errorf("remove restored %s: %w", entry.Target, err)
			}
			report.Add(Result{Kind: "file", ID: entry.ID, Path: entry.Target, Status: "removed"})
			removedTargets = append(removedTargets, entry.Target)
			report.Files++
		case "present":
			// streamed revert: temp + rename, digest-verified by decryptFile
			if err := decryptFile(undoObjectPath(repository, entry.ID), entry.Target,
				keyBytes, "undo-"+entry.ID, entry.PreSHA256, entry.PreSize); err != nil {
				return report, fmt.Errorf("revert %s: %w", entry.Target, err)
			}
			if err := osChmod(entry.Target, os.FileMode(entry.PreMode)); err != nil {
				return report, fmt.Errorf("revert permissions %s: %w", entry.Target, err)
			}
			report.Add(Result{Kind: "file", ID: entry.ID, Path: entry.Target, Status: "reverted"})
			report.Files++
		}
	}
	// a consumed journal makes its backup objects useless — delete them so
	// the repository does not accumulate orphaned pre-state copies
	for _, entry := range journal.Entries {
		_ = os.Remove(undoObjectPath(repository, entry.ID))
	}
	// remove the empty directory skeleton the restore created, walking up
	// from each removed file to the destination root; pre-existing dirs
	// with their own content fail os.Remove and stop the walk naturally
	for _, target := range removedTargets {
		removeEmptyAncestors(target, journal.Destination)
	}
	if undone > 0 {
		if err := SaveRestoreState(repository, keyBytes, state); err != nil {
			return report, fmt.Errorf("record undone state: %w", err)
		}
	}
	if err := os.Remove(undoJournalPath(repository, journal.Destination)); err != nil {
		return report, fmt.Errorf("consume undo journal: %w", err)
	}
	report.Add(Result{Kind: "limitation", Status: "not-reverted", Detail: "applications and settings"})
	report.Finish()
	return report, nil
}

// removeEmptyAncestors deletes now-empty directories from path's parent up
// to (but never including) stopRoot. os.Remove on a non-empty directory
// fails, so pre-existing content stops the walk by itself.
func removeEmptyAncestors(path, stopRoot string) {
	for dir := filepath.Dir(path); dir != "" && dir != "." && dir != string(filepath.Separator); dir = filepath.Dir(dir) {
		if stopRoot != "" && !isWithin(dir, stopRoot) {
			return
		}
		if err := os.Remove(dir); err != nil {
			return // non-empty, protected, or already gone: stop climbing
		}
	}
}
