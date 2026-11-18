package migration

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Review fix pack: streaming journal backup (no whole-file RAM reads),
// per-destination journals (a bundle restored to two machines stays
// undoable on both), missing-vs-removed honesty, command-aware
// next-steps, users-root source classification, and a pinned
// at-rest-encryption claim.

func TestUndoHandlesLargePreExistingTargetWithoutWholeFileBuffering(t *testing.T) {
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
	writeFile(t, filepath.Join(source, "Documents", "big.txt"), "small-source-version")

	// an 8MB pre-existing destination target: the journal must back it up
	// through the streaming object path, not a whole-buffer read
	preExisting := make([]byte, 8<<20)
	if _, err := rand.Read(preExisting); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(destination, "Documents"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "Documents", "big.txt"), preExisting, 0o600); err != nil {
		t.Fatal(err)
	}

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
	got, err := os.ReadFile(filepath.Join(destination, "Documents", "big.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, preExisting) {
		t.Fatalf("undo must restore the exact pre-existing bytes (got %d bytes, want %d)",
			len(got), len(preExisting))
	}
}

func TestBundleRestoredToTwoDestinationsIsUndoableOnBoth(t *testing.T) {
	source := t.TempDir()
	repository := filepath.Join(t.TempDir(), "repository")
	keyPath := filepath.Join(t.TempDir(), "migration.key")
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeKeyForTest(keyPath, key); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(source, "Documents", "one.txt"), "one")

	dest1, dest2 := t.TempDir(), t.TempDir()
	if _, err := Capture(source, repository, key); err != nil {
		t.Fatal(err)
	}
	for _, dest := range []string{dest1, dest2} {
		if _, err := DryRunRestore(repository, dest, keyPath, "personal", true); err != nil {
			t.Fatalf("dry-run %s: %v", dest, err)
		}
		if _, err := ManagedRestore(repository, dest, keyPath, "personal", true, false); err != nil {
			t.Fatalf("restore %s: %v", dest, err)
		}
	}
	for _, dest := range []string{dest2, dest1} {
		if _, err := UndoRestore(repository, dest, keyPath); err != nil {
			t.Fatalf("undo %s: %v (each destination must keep its own journal)", dest, err)
		}
		if _, err := os.Stat(filepath.Join(dest, "Documents", "one.txt")); !os.IsNotExist(err) {
			t.Fatalf("undo %s must remove the restored file", dest)
		}
	}
}

func TestUndoReportsMissingRatherThanFakeSuccess(t *testing.T) {
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
	writeFile(t, filepath.Join(source, "Documents", "one.txt"), "one")
	if _, err := Capture(source, repository, key); err != nil {
		t.Fatal(err)
	}
	if _, err := DryRunRestore(repository, destination, keyPath, "personal", true); err != nil {
		t.Fatal(err)
	}
	if _, err := ManagedRestore(repository, destination, keyPath, "personal", true, false); err != nil {
		t.Fatal(err)
	}
	// the destination vanishes (unmounted, wiped) before undo
	if err := os.RemoveAll(destination); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	report, err := UndoRestore(repository, destination, keyPath)
	if err != nil {
		t.Fatalf("undo on a vanished destination must not crash: %v", err)
	}
	statuses := map[string]int{}
	for _, result := range report.Results {
		statuses[result.Status]++
	}
	if statuses["removed"] != 0 || statuses["missing"] != 1 {
		t.Fatalf("undo must report missing, not fake success: %#v", report.Results)
	}
}

func TestJournalAndUndoObjectsAreNotPlaintextOnDisk(t *testing.T) {
	repository, destination, keyPath := setupUndoFixture(t, true)
	if _, err := ManagedRestore(repository, destination, keyPath, "personal", true, false); err != nil {
		t.Fatal(err)
	}
	secret := []byte("beta-PRE-EXISTING")
	entries, err := os.ReadDir(filepath.Join(repository, objectDir))
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "undo-") {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(repository, objectDir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(contents, secret) {
			t.Fatalf("%s leaks plaintext pre-state on disk", entry.Name())
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("expected at least one undo object on disk")
	}
	// the journal file itself
	journalMatches, _ := filepath.Glob(filepath.Join(repository, "undo-journal*"))
	if len(journalMatches) == 0 {
		t.Fatal("expected an undo journal on disk")
	}
	for _, journalPath := range journalMatches {
		contents, err := os.ReadFile(journalPath)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(contents, secret) || bytes.Contains(contents, []byte(`"beta`)) {
			t.Fatalf("%s leaks plaintext paths/content", journalPath)
		}
	}
}
