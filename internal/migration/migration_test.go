package migration

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestEncryptBytesRoundTripAndTamperDetection(t *testing.T) {
	key := bytes.Repeat([]byte{7}, KeySize)
	plaintext := []byte("private migration metadata")

	ciphertext, err := encryptBytes(key, plaintext, []byte("manifest-v1"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	got, err := decryptBytes(key, ciphertext, []byte("manifest-v1"))
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("round trip mismatch: got %q", got)
	}

	ciphertext[len(ciphertext)-1] ^= 1
	if _, err := decryptBytes(key, ciphertext, []byte("manifest-v1")); err == nil {
		t.Fatal("tampered ciphertext was accepted")
	}
}

func TestInventoryExcludesRepositoryAndCredentialLikeFiles(t *testing.T) {
	source := t.TempDir()
	repository := filepath.Join(source, "migration-repository")
	if err := os.Mkdir(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(source, "Documents", "notes.txt"), "keep")
	writeFile(t, filepath.Join(source, ".ssh", "id_ed25519"), "private")
	writeFile(t, filepath.Join(source, "migration-repository", "stale.bin"), "do not copy")

	items, exclusions, err := inventory(source, repository)
	if err != nil {
		t.Fatalf("inventory: %v", err)
	}
	if len(items) != 1 || items[0].RelativePath != filepath.ToSlash(filepath.Join("Documents", "notes.txt")) {
		t.Fatalf("unexpected inventory: %#v", items)
	}
	if len(exclusions) == 0 {
		t.Fatal("expected excluded paths to be reported")
	}
}

func TestCaptureRestoreAndVerify(t *testing.T) {
	source := t.TempDir()
	repository := filepath.Join(t.TempDir(), "repository")
	destination := t.TempDir()
	keyPath := filepath.Join(t.TempDir(), "migration.key")
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(source, "Documents", "notes.txt"), "keep this file")
	writeFile(t, filepath.Join(source, "Pictures", "photo.bin"), "binary-ish content")

	manifest, err := Capture(source, repository, key)
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if len(manifest.Items) != 2 {
		t.Fatalf("expected two captured files, got %d", len(manifest.Items))
	}
	if err := Verify(repository, key); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := Restore(repository, destination, key, false, false); err != nil {
		t.Fatalf("restore: %v", err)
	}
	assertFile(t, filepath.Join(destination, "Documents", "notes.txt"), "keep this file")
	assertFile(t, filepath.Join(destination, "Pictures", "photo.bin"), "binary-ish content")

	if runtime.GOOS != "windows" {
		if mode := fileMode(t, keyPath); mode.Perm() != 0o600 {
			t.Fatalf("key permissions: %o", mode.Perm())
		}
	}
}

func TestPlanRestoreRejectsTraversal(t *testing.T) {
	_, err := PlanRestore(Manifest{Items: []Item{{RelativePath: "../outside.txt"}}}, t.TempDir(), false)
	if err == nil {
		t.Fatal("path traversal was accepted")
	}
}

func TestDryRunDoesNotCreateDestinationFiles(t *testing.T) {
	source := t.TempDir()
	repository := filepath.Join(t.TempDir(), "repository")
	destination := filepath.Join(t.TempDir(), "destination")
	key := bytes.Repeat([]byte{9}, KeySize)
	writeFile(t, filepath.Join(source, "file.txt"), "content")
	if _, err := Capture(source, repository, key); err != nil {
		t.Fatalf("capture: %v", err)
	}
	if err := Restore(repository, destination, key, false, true); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "file.txt")); !os.IsNotExist(err) {
		t.Fatalf("dry run created destination file: %v", err)
	}
}

func TestRestoreRejectsSymlinkedDestinationDirectory(t *testing.T) {
	source := t.TempDir()
	repository := filepath.Join(t.TempDir(), "repository")
	destination := t.TempDir()
	outside := t.TempDir()
	key := bytes.Repeat([]byte{11}, KeySize)
	writeFile(t, filepath.Join(source, "Documents", "file.txt"), "content")
	if _, err := Capture(source, repository, key); err != nil {
		t.Fatalf("capture: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(destination, "Documents")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := Restore(repository, destination, key, true, false); err == nil {
		t.Fatal("restore followed symlinked destination directory")
	}
}

func TestManagedRestoreRequiresDryRunAndRestoresSettings(t *testing.T) {
	source := t.TempDir()
	repository := filepath.Join(t.TempDir(), "repository")
	destination := t.TempDir()
	keyPath := filepath.Join(t.TempDir(), "migration.key")
	key := bytes.Repeat([]byte{13}, KeySize)
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(source, "file.txt"), "content")
	writeFile(t, filepath.Join(source, ".gitconfig"), "[user]\n\tname = Example\n\temail = example@example.com\n[credential]\n\thelper = store\n")
	if _, err := Capture(source, repository, key); err != nil {
		t.Fatal(err)
	}
	if _, err := ManagedRestore(repository, destination, keyPath, "personal", false, false); err == nil {
		t.Fatal("restore succeeded without dry-run")
	}
	if _, err := DryRunRestore(repository, destination, keyPath, "personal", false); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	report, err := ManagedRestore(repository, destination, keyPath, "personal", false, false)
	if err != nil {
		t.Fatalf("managed restore: %v", err)
	}
	if len(report.Results) == 0 {
		t.Fatal("managed restore returned no results")
	}
	content, err := os.ReadFile(filepath.Join(destination, ".gitconfig"))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if string(content) != "[user]\n\tname = Example\n\temail = example@example.com\n" {
		t.Fatalf("unexpected settings: %s", content)
	}
	if raw, err := os.ReadFile(filepath.Join(repository, restoreStateFile)); err != nil {
		t.Fatal(err)
	} else if bytes.Contains(raw, []byte("Example")) {
		t.Fatal("restore state contains plaintext settings")
	}
}

func TestVerifyDestinationDetectsModifiedFile(t *testing.T) {
	source := t.TempDir()
	repository := filepath.Join(t.TempDir(), "repository")
	destination := t.TempDir()
	key := bytes.Repeat([]byte{15}, KeySize)
	writeFile(t, filepath.Join(source, "file.txt"), "original")
	if _, err := Capture(source, repository, key); err != nil {
		t.Fatal(err)
	}
	if err := Restore(repository, destination, key, false, false); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(destination, "file.txt"), "modified")
	results, err := VerifyDestination(repository, destination, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Status != "checksum-mismatch" {
		t.Fatalf("unexpected verification results: %#v", results)
	}
}

func TestResumeUsesCompletedState(t *testing.T) {
	source := t.TempDir()
	repository := filepath.Join(t.TempDir(), "repository")
	destination := t.TempDir()
	keyPath := filepath.Join(t.TempDir(), "migration.key")
	key := bytes.Repeat([]byte{17}, KeySize)
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(source, "file.txt"), "resume")
	if _, err := Capture(source, repository, key); err != nil {
		t.Fatal(err)
	}
	if _, err := DryRunRestore(repository, destination, keyPath, "personal", false); err != nil {
		t.Fatal(err)
	}
	if _, err := ResumeRestore(repository, keyPath, "personal", false); err != nil {
		t.Fatal(err)
	}
	if _, err := ResumeRestore(repository, keyPath, "personal", false); err != nil {
		t.Fatal(err)
	}
	assertFile(t, filepath.Join(destination, "file.txt"), "resume")
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertFile(t *testing.T, path, expected string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != expected {
		t.Fatalf("%s: got %q, want %q", path, contents, expected)
	}
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode()
}
