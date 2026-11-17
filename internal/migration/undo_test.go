package migration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Steal: PCmover Professional's paid-tier differentiator is Undo. The
// restore plan already enumerates every target; an undo journal is that
// plan's pre-state, captured before the copy loop runs and stored
// encrypted in the repository. Only files the restore actually copied are
// reverted — skipped targets are untouched. Honest limitation, stated in
// every undo report: applications and settings are not reverted.

func setupUndoFixture(t *testing.T, overwrite bool) (repository, destination, keyPath string) {
	t.Helper()
	source := t.TempDir()
	repository = filepath.Join(t.TempDir(), "repository")
	destination = t.TempDir()
	keyPath = filepath.Join(t.TempDir(), "migration.key")
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeKeyForTest(keyPath, key); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(source, "Documents", "alpha.txt"), "alpha-original")
	writeFile(t, filepath.Join(source, "Documents", "beta.txt"), "beta-from-source")
	writeFile(t, filepath.Join(source, "Pictures", "gamma.txt"), "gamma-original")

	// destination already contains beta.txt with DIFFERENT content
	writeFile(t, filepath.Join(destination, "Documents", "beta.txt"), "beta-PRE-EXISTING")

	if _, err := Capture(source, repository, key); err != nil {
		t.Fatalf("capture: %v", err)
	}
	if _, err := DryRunRestore(repository, destination, keyPath, "personal", overwrite); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	return repository, destination, keyPath
}

func readOrFail(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(contents)
}

func TestUndoRevertsRestoreToPreState(t *testing.T) {
	repository, destination, keyPath := setupUndoFixture(t, true)

	report, err := ManagedRestore(repository, destination, keyPath, "personal", true, false)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if report.Files != 3 {
		t.Fatalf("restore should copy 3 files, got %d", report.Files)
	}
	// overwrite=true replaced beta's pre-existing content
	if got := readOrFail(t, filepath.Join(destination, "Documents", "beta.txt")); got != "beta-from-source" {
		t.Fatalf("restore should overwrite beta, got %q", got)
	}

	undoReport, err := UndoRestore(repository, destination, keyPath)
	if err != nil {
		t.Fatalf("undo: %v", err)
	}
	// absent targets: the restore's files are gone
	for _, name := range []string{"Documents/alpha.txt", "Pictures/gamma.txt"} {
		if _, err := os.Stat(filepath.Join(destination, filepath.FromSlash(name))); !os.IsNotExist(err) {
			t.Fatalf("undo must remove restored-only file %s", name)
		}
	}
	// present target: the PRE-EXISTING bytes are back
	if got := readOrFail(t, filepath.Join(destination, "Documents", "beta.txt")); got != "beta-PRE-EXISTING" {
		t.Fatalf("undo must restore beta's original bytes, got %q", got)
	}
	if undoReport.Command != "undo" {
		t.Fatalf("undo report command: %q", undoReport.Command)
	}
	joined := strings.Join(summaryLines(undoReport), "\n")
	if !strings.Contains(joined, "reverted") && !strings.Contains(joined, "removed") {
		t.Fatalf("undo report must state what it did:\n%s", joined)
	}
	if !strings.Contains(joined, "not-reverted") {
		t.Fatalf("undo report must state the applications/settings limitation:\n%s", joined)
	}
}

func summaryLines(report Report) []string {
	var out []string
	for _, result := range report.Results {
		out = append(out, result.Kind+" "+result.Status+" "+result.Path)
	}
	return out
}

func TestSecondUndoFailsLoudly(t *testing.T) {
	repository, destination, keyPath := setupUndoFixture(t, true)
	if _, err := ManagedRestore(repository, destination, keyPath, "personal", true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := UndoRestore(repository, destination, keyPath); err != nil {
		t.Fatalf("first undo: %v", err)
	}
	if _, err := UndoRestore(repository, destination, keyPath); err == nil {
		t.Fatal("second undo must fail loudly, not silently no-op")
	}
}

func TestUndoWithoutPriorRestoreFailsLoudly(t *testing.T) {
	repository, destination, keyPath := setupUndoFixture(t, true) // dry-run only, no restore
	if _, err := UndoRestore(repository, destination, keyPath); err == nil {
		t.Fatal("undo with no prior restore must fail loudly")
	}
}

func TestUndoLeavesSkippedTargetsUntouched(t *testing.T) {
	repository, destination, keyPath := setupUndoFixture(t, false) // overwrite=false
	if _, err := ManagedRestore(repository, destination, keyPath, "personal", false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := UndoRestore(repository, destination, keyPath); err != nil {
		t.Fatalf("undo: %v", err)
	}
	// beta was skipped-existing: the restore never changed it, undo must not either
	if got := readOrFail(t, filepath.Join(destination, "Documents", "beta.txt")); got != "beta-PRE-EXISTING" {
		t.Fatalf("skipped target must stay untouched, got %q", got)
	}
	for _, name := range []string{"Documents/alpha.txt", "Pictures/gamma.txt"} {
		if _, err := os.Stat(filepath.Join(destination, filepath.FromSlash(name))); !os.IsNotExist(err) {
			t.Fatalf("undo must still remove restored-only file %s", name)
		}
	}
}

func TestUndoResumesAcrossInterruptedRestore(t *testing.T) {
	repository, destination, keyPath := setupUndoFixture(t, true)
	// first restore pass completes; resume re-runs (idempotent) — the
	// journal must survive the resume and still undo cleanly
	if _, err := ManagedRestore(repository, destination, keyPath, "personal", true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := ResumeRestore(repository, keyPath, "personal", false); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if _, err := UndoRestore(repository, destination, keyPath); err != nil {
		t.Fatalf("undo after resume: %v", err)
	}
	if got := readOrFail(t, filepath.Join(destination, "Documents", "beta.txt")); got != "beta-PRE-EXISTING" {
		t.Fatalf("undo after resume must restore original bytes, got %q", got)
	}
}
