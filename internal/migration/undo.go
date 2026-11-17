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
// encrypted in the repository (same encryptBytes path as the manifest).
// Only files the restore actually copied are reverted — skipped targets
// are untouched. Honest limitation, stated in every undo report:
// applications and settings are not reverted.

const undoJournalFile = "undo-journal.enc"

type undoEntry struct {
	ID        string `json:"id"`
	Target    string `json:"target"`
	Pre       string `json:"pre"` // "absent" | "present"
	PreSHA256 string `json:"pre_sha256,omitempty"`
	PreMode   uint32 `json:"pre_mode,omitempty"`
}

type undoJournal struct {
	Version     int         `json:"version"`
	Destination string      `json:"destination"`
	Entries     []undoEntry `json:"entries"`
}

func undoObjectPath(repository, id string) string {
	return filepath.Join(repository, objectDir, "undo-"+id+".bin")
}

func writeUndoJournal(repository string, key []byte, destination string, plans []CopyPlan) error {
	journalPath := filepath.Join(repository, undoJournalFile)
	if _, err := os.Stat(journalPath); err == nil {
		// A journal from an earlier (possibly interrupted) pass stands:
		// the pre-state it recorded is the truth undo must revert to.
		return nil
	}
	journal := undoJournal{Version: 1, Destination: destination}
	for _, plan := range plans {
		entry := undoEntry{ID: plan.Item.ID, Target: plan.Target, Pre: "absent"}
		if info, statErr := os.Stat(plan.Target); statErr == nil && info.Mode().IsRegular() {
			contents, readErr := os.ReadFile(plan.Target)
			if readErr != nil {
				return fmt.Errorf("back up pre-existing %s: %w", plan.Item.RelativePath, readErr)
			}
			ciphertext, encErr := encryptBytes(key, contents, []byte("undo-"+plan.Item.ID))
			if encErr != nil {
				return fmt.Errorf("encrypt undo object %s: %w", plan.Item.ID, encErr)
			}
			if writeErr := os.WriteFile(undoObjectPath(repository, plan.Item.ID), ciphertext, 0o600); writeErr != nil {
				return fmt.Errorf("write undo object %s: %w", plan.Item.ID, writeErr)
			}
			digest := sha256.Sum256(contents)
			entry.Pre = "present"
			entry.PreSHA256 = hex.EncodeToString(digest[:])
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
	if err := os.WriteFile(journalPath, ciphertext, 0o600); err != nil {
		return fmt.Errorf("write undo journal: %w", err)
	}
	return nil
}

func loadUndoJournal(repository string, key []byte) (undoJournal, error) {
	ciphertext, err := os.ReadFile(filepath.Join(repository, undoJournalFile))
	if err != nil {
		return undoJournal{}, errors.New("nothing to undo: no restore journal in the repository")
	}
	plaintext, err := decryptBytes(key, ciphertext, []byte("undo-journal-v1"))
	if err != nil {
		return undoJournal{}, fmt.Errorf("decrypt undo journal: %w", err)
	}
	var journal undoJournal
	if err := json.Unmarshal(plaintext, &journal); err != nil {
		return undoJournal{}, fmt.Errorf("decode undo journal: %w", err)
	}
	return journal, nil
}

// UndoRestore reverts the effects of the last managed restore: files the
// restore copied are removed (absent pre-state) or replaced with their
// original bytes (present pre-state, verified against the recorded
// SHA-256). The journal is consumed on success; a second undo fails
// loudly. Applications and settings are NOT reverted — the report says so.
func UndoRestore(repository, destination, key string) (Report, error) {
	keyBytes, err := ReadKey(key)
	if err != nil {
		return Report{}, err
	}
	journal, err := loadUndoJournal(repository, keyBytes)
	if err != nil {
		return Report{}, err
	}
	if destination != "" && journal.Destination != destination {
		return Report{}, fmt.Errorf("journal was recorded for destination %s, not %s",
			journal.Destination, destination)
	}
	state, err := LoadRestoreState(repository, keyBytes)
	if err != nil {
		return Report{}, fmt.Errorf("load restore state: %w", err)
	}
	report := NewReport("undo")
	report.Destination = journal.Destination
	for _, entry := range journal.Entries {
		current, ok := state.Items[entry.ID]
		if !ok || current.Status != "copied" {
			continue // the restore never changed this target
		}
		switch entry.Pre {
		case "absent":
			if err := os.Remove(entry.Target); err != nil && !os.IsNotExist(err) {
				return report, fmt.Errorf("remove restored %s: %w", entry.Target, err)
			}
			report.Add(Result{Kind: "file", ID: entry.ID, Path: entry.Target, Status: "removed"})
			report.Files++
		case "present":
			ciphertext, readErr := os.ReadFile(undoObjectPath(repository, entry.ID))
			if readErr != nil {
				return report, fmt.Errorf("read undo object %s: %w", entry.ID, readErr)
			}
			contents, decErr := decryptBytes(keyBytes, ciphertext, []byte("undo-"+entry.ID))
			if decErr != nil {
				return report, fmt.Errorf("decrypt undo object %s: %w", entry.ID, decErr)
			}
			digest := sha256.Sum256(contents)
			if hex.EncodeToString(digest[:]) != entry.PreSHA256 {
				return report, fmt.Errorf("undo object %s failed integrity check", entry.ID)
			}
			if writeErr := os.WriteFile(entry.Target, contents, os.FileMode(entry.PreMode)); writeErr != nil {
				return report, fmt.Errorf("revert %s: %w", entry.Target, writeErr)
			}
			report.Add(Result{Kind: "file", ID: entry.ID, Path: entry.Target, Status: "reverted"})
			report.Files++
		}
	}
	if err := os.Remove(filepath.Join(repository, undoJournalFile)); err != nil {
		return report, fmt.Errorf("consume undo journal: %w", err)
	}
	report.Add(Result{Kind: "limitation", Status: "not-reverted", Detail: "applications and settings"})
	report.Finish()
	return report, nil
}
