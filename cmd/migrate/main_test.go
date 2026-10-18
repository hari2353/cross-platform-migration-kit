package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/haris/cross-platform-migration-kit/internal/migration"
)

func TestVerifyDestinationFailsOnMissingOrModifiedFiles(t *testing.T) {
	silenceStdout(t)
	source := t.TempDir()
	repository := filepath.Join(t.TempDir(), "repository")
	destination := filepath.Join(t.TempDir(), "destination")
	root := t.TempDir()
	keyPath := filepath.Join(root, "migration.key")
	key := bytes.Repeat([]byte{31}, migration.KeySize)
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(source, "Documents", "file.txt"), "content")
	if _, err := migration.Capture(source, repository, key); err != nil {
		t.Fatalf("capture: %v", err)
	}

	if _, err := migration.DryRunRestore(repository, destination, keyPath, "personal", false); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if _, err := migration.ManagedRestore(repository, destination, keyPath, "personal", false, false); err != nil {
		t.Fatalf("restore: %v", err)
	}

	if err := run([]string{
		"verify", "--bundle", repository, "--key-file", keyPath, "--destination", destination, "--json",
	}); err != nil {
		t.Fatalf("clean destination verify failed: %v", err)
	}

	// A modified file must make the CLI fail, per spec success criterion 7.
	writeFixture(t, filepath.Join(destination, "Documents", "file.txt"), "tampered")
	if err := run([]string{
		"verify", "--bundle", repository, "--key-file", keyPath, "--destination", destination, "--json",
	}); err == nil {
		t.Fatal("verify accepted a modified destination file")
	}

	// A missing file must also make the CLI fail.
	if err := os.Remove(filepath.Join(destination, "Documents", "file.txt")); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{
		"verify", "--bundle", repository, "--key-file", keyPath, "--destination", destination, "--json",
	}); err == nil {
		t.Fatal("verify accepted a missing destination file")
	}
}

func TestVerifyHumanReportReportsFailures(t *testing.T) {
	silenceStdout(t)
	source := t.TempDir()
	repository := filepath.Join(t.TempDir(), "repository")
	destination := filepath.Join(t.TempDir(), "destination")
	root := t.TempDir()
	keyPath := filepath.Join(root, "migration.key")
	key := bytes.Repeat([]byte{33}, migration.KeySize)
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(source, "file.txt"), "content")
	if _, err := migration.Capture(source, repository, key); err != nil {
		t.Fatal(err)
	}
	if _, err := migration.DryRunRestore(repository, destination, keyPath, "personal", false); err != nil {
		t.Fatal(err)
	}
	if _, err := migration.ManagedRestore(repository, destination, keyPath, "personal", false, false); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(destination, "file.txt"), "tampered")
	if err := run([]string{
		"verify", "--bundle", repository, "--key-file", keyPath, "--destination", destination,
	}); err == nil {
		t.Fatal("verify accepted a modified destination file")
	}
}

func TestDoctorFailsOnUnreadableBundle(t *testing.T) {
	silenceStdout(t)
	empty := t.TempDir()
	if err := run([]string{"doctor", "--bundle", empty, "--key-file", filepath.Join(empty, "missing.key")}); err == nil {
		t.Fatal("doctor accepted a missing bundle and key")
	}
}

func TestCaptureRequiresRepositoryAndKey(t *testing.T) {
	if err := run([]string{"capture", "--source", t.TempDir(), "--key-file", "x.key"}); err == nil {
		t.Fatal("capture accepted missing --output")
	}
	if err := run([]string{"capture", "--output", t.TempDir()}); err == nil {
		t.Fatal("capture accepted missing --key-file")
	}
	if err := run([]string{"capture", "--output", t.TempDir(), "--key-file", "x.key", "--profile", "unknown"}); err == nil {
		t.Fatal("capture accepted an unknown profile")
	}
}

func TestRestoreAndResumeRejectUnknownProfile(t *testing.T) {
	repository := t.TempDir()
	keyPath := "nonexistent.key"
	if err := run([]string{"restore", "--bundle", repository, "--key-file", keyPath, "--profile", "everything"}); err == nil {
		t.Fatal("restore accepted an unknown profile")
	}
	if err := run([]string{"resume", "--bundle", repository, "--key-file", keyPath, "--profile", "everything"}); err == nil {
		t.Fatal("resume accepted an unknown profile")
	}
}

func TestKeygenRefusesToOverwrite(t *testing.T) {
	silenceStdout(t)
	root := t.TempDir()
	keyPath := filepath.Join(root, "migration.key")
	if err := run([]string{"keygen", "--output", keyPath}); err != nil {
		t.Fatalf("keygen: %v", err)
	}
	contents, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(contents) != migration.KeySize {
		t.Fatalf("key is %d bytes, want %d", len(contents), migration.KeySize)
	}
	if err := run([]string{"keygen", "--output", keyPath}); err == nil {
		t.Fatal("keygen overwrote an existing key")
	}
}

func TestUnknownCommandFails(t *testing.T) {
	if err := run([]string{"teleport"}); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("unexpected error for unknown command: %v", err)
	}
}

func TestRestoreWithoutDryRunFails(t *testing.T) {
	source := t.TempDir()
	repository := filepath.Join(t.TempDir(), "repository")
	destination := filepath.Join(t.TempDir(), "destination")
	root := t.TempDir()
	keyPath := filepath.Join(root, "migration.key")
	key := bytes.Repeat([]byte{35}, migration.KeySize)
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(source, "file.txt"), "content")
	if _, err := migration.Capture(source, repository, key); err != nil {
		t.Fatal(err)
	}
	err := run([]string{"restore", "--bundle", repository, "--destination", destination, "--key-file", keyPath})
	if err == nil || !strings.Contains(err.Error(), "dry-run") {
		t.Fatalf("restore without dry-run returned: %v", err)
	}
}

// writeFixture keeps stdout quiet during tests and creates fixture files.
func writeFixture(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

// silenceStdout redirects run() output during tests that only assert errors.
func silenceStdout(t *testing.T) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = writer
	drain := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, reader)
		close(drain)
	}()
	t.Cleanup(func() {
		os.Stdout = saved
		writer.Close()
		<-drain
		reader.Close()
	})
}

func TestVerifyReportMentionsProblemsInHumanMode(t *testing.T) {
	source := t.TempDir()
	repository := filepath.Join(t.TempDir(), "repository")
	destination := filepath.Join(t.TempDir(), "destination")
	root := t.TempDir()
	keyPath := filepath.Join(root, "migration.key")
	key := bytes.Repeat([]byte{37}, migration.KeySize)
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(source, "file.txt"), "content")
	if _, err := migration.Capture(source, repository, key); err != nil {
		t.Fatal(err)
	}
	if _, err := migration.DryRunRestore(repository, destination, keyPath, "personal", false); err != nil {
		t.Fatal(err)
	}
	if _, err := migration.ManagedRestore(repository, destination, keyPath, "personal", false, false); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(destination, "file.txt"), "tampered")
	saved := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	err = run([]string{"verify", "--bundle", repository, "--key-file", keyPath, "--destination", destination})
	writer.Close()
	os.Stdout = saved
	if err == nil {
		t.Fatal("verify accepted a modified destination file")
	}
	output, readErr := io.ReadAll(reader)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(output), "checksum-mismatch") {
		t.Fatalf("human report did not explain the failure: %s", output)
	}
}
