package migration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Deferred-minor fix pack from the whole-branch review.

func TestUndoRemovesEmptyDirectorySkeletonItCreated(t *testing.T) {
	source := t.TempDir()
	repository := filepath.Join(t.TempDir(), "repository")
	destination := t.TempDir()
	keyPath := filepath.Join(t.TempDir(), "migration.key")
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeKeyForTest(keyPath, key); err != nil {
		t.Fatal(err)
	}
	// deep restore-created path; destination also PRE-has a sibling dir
	// with its own content, which must survive
	writeFile(t, filepath.Join(source, "Documents", "deep", "nested", "file.txt"), "x")
	if _, err := Capture(source, repository, key); err != nil {
		t.Fatal(err)
	}
	if _, err := DryRunRestore(repository, destination, keyPath, "personal", true); err != nil {
		t.Fatal(err)
	}
	if _, err := ManagedRestore(repository, destination, keyPath, "personal", true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := UndoRestore(repository, destination, keyPath); err != nil {
		t.Fatalf("undo: %v", err)
	}
	for _, gone := range []string{
		filepath.Join(destination, "Documents", "deep", "nested"),
		filepath.Join(destination, "Documents", "deep"),
	} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("undo must remove the empty skeleton dir %s", gone)
		}
	}
	// a pre-existing directory with other content is untouched
	kept := filepath.Join(destination, "Documents", "keep")
	if err := os.MkdirAll(kept, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kept, "mine.txt"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := UndoRestore(repository, destination, keyPath); err == nil {
		t.Fatal("second undo must fail (journal consumed)")
	}
	if _, err := os.Stat(filepath.Join(kept, "mine.txt")); err != nil {
		t.Fatalf("pre-existing content must never be touched by undo: %v", err)
	}
}

func TestUndoMarksStateUndoneAndCleansOrphanObjects(t *testing.T) {
	repository, destination, keyPath := setupUndoFixture(t, true)
	if _, err := ManagedRestore(repository, destination, keyPath, "personal", true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := UndoRestore(repository, destination, keyPath); err != nil {
		t.Fatal(err)
	}
	state, err := LoadRestoreState(repository, mustKeyBytes(t, keyPath))
	if err != nil {
		t.Fatal(err)
	}
	for id, item := range state.Items {
		if item.UndoneIn != destination {
			t.Errorf("state item %s does not record the undone destination (%q) — resume would silently re-copy", id, item.UndoneIn)
		}
	}
	entries, err := os.ReadDir(filepath.Join(repository, objectDir))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "undo-") {
			t.Errorf("orphaned undo object %s left in the repository", entry.Name())
		}
	}
}

func mustKeyBytes(t *testing.T, keyPath string) []byte {
	t.Helper()
	key, err := ReadKey(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	return key
}
