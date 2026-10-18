package migration

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

	items, exclusions, err := inventoryWithOptions(source, repository, false)
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

func exclusionReason(exclusions []Exclusion, relative string) (string, bool) {
	for _, exclusion := range exclusions {
		if filepath.ToSlash(exclusion.RelativePath) == relative {
			return exclusion.Reason, true
		}
	}
	return "", false
}

func TestInventoryAppliesProtectedPathPolicy(t *testing.T) {
	source := t.TempDir()
	excluded := map[string]string{
		"AppData/Local/Temp/junk.tmp":              "temp",
		"AppData/Local/Google/Chrome/User Data/c":  "browser",
		"AppData/Local/CrashDumps/crash.dmp":       "dump",
		"AppData/Local/npm-cache/pkg.tar":          "npmcache",
		"AppData/Roaming/Thunderbird/profiles.ini": "thunderbird",
		"ntuser.dat":                               "hive",
		"ntuser.dat.log1":                          "hive belt",
		"Thumbs.db":                                "thumbs",
		"Library/Caches/com.apple.Safari/cache.db": "mac cache",
		".Trash/old.txt":                           "trash",
		".DS_Store":                                "finder",
		"Projects/demo/.cache/tool.bin":            "nested xdg cache",
		"Projects/demo/__pycache__/x.cpython.pyc":  "bytecode",
		"go/pkg/mod/golang.org/tool@v1/tool.go":    "module cache",
	}
	for relative, contents := range excluded {
		writeFile(t, filepath.Join(source, filepath.FromSlash(relative)), contents)
	}
	// Personal data that must NOT be excluded.
	kept := []string{filepath.ToSlash(filepath.Join("Documents", "keep.txt")), filepath.ToSlash(filepath.Join("Projects", "demo", "main.go"))}
	for _, relative := range kept {
		writeFile(t, filepath.Join(source, filepath.FromSlash(relative)), "keep")
	}

	items, exclusions, err := inventoryWithOptions(source, t.TempDir(), false)
	if err != nil {
		t.Fatalf("inventory: %v", err)
	}
	// The walk records the top-most excluded path (the prefix directory itself
	// for prefix rules, the file for name rules) and never captures below it.
	for relative := range excluded {
		covered := false
		for _, exclusion := range exclusions {
			if path := filepath.ToSlash(exclusion.RelativePath); relative == path || strings.HasPrefix(relative, path+"/") {
				covered = true
				break
			}
		}
		if !covered {
			t.Errorf("path %q was not excluded by policy", relative)
		}
	}
	for _, item := range items {
		if _, ok := exclusionReason(exclusions, item.RelativePath); ok {
			continue
		}
		found := false
		for _, relative := range kept {
			if item.RelativePath == relative {
				found = true
			}
		}
		if !found {
			t.Errorf("unexpected captured item: %q", item.RelativePath)
		}
	}
	if len(items) != 2 {
		t.Errorf("expected only personal data to be captured, got %#v", items)
	}
	if reason, ok := exclusionReason(exclusions, "AppData/Local/Temp"); !ok || reason != "volatile temporary files" {
		t.Errorf("unexpected exclusion reason for temp dir: %q %v", reason, ok)
	}
}

func TestCaptureReRunsIntoExistingRepository(t *testing.T) {
	source := t.TempDir()
	repository := filepath.Join(t.TempDir(), "repository")
	key := bytes.Repeat([]byte{21}, KeySize)
	writeFile(t, filepath.Join(source, "Documents", "resume.txt"), "first capture")

	if _, err := Capture(source, repository, key); err != nil {
		t.Fatalf("initial capture: %v", err)
	}
	// Re-running capture into the same repository (for example after an
	// interruption or a second run) must succeed without duplicating objects.
	manifest, err := Capture(source, repository, key)
	if err != nil {
		t.Fatalf("idempotent capture: %v", err)
	}
	if len(manifest.Items) != 1 {
		t.Fatalf("unexpected item count: %#v", manifest.Items)
	}
	if err := Verify(repository, key); err != nil {
		t.Fatalf("verify after re-capture: %v", err)
	}

	// A tampered object inside the repository is replaced, not silently reused.
	objectPath := filepath.Join(repository, objectDir, manifest.Items[0].ID+".bin")
	raw, err := os.ReadFile(objectPath)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0xFF
	if err := os.WriteFile(objectPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Capture(source, repository, key); err != nil {
		t.Fatalf("capture with tampered object: %v", err)
	}
	if err := Verify(repository, key); err != nil {
		t.Fatalf("tampered object was not repaired during re-capture: %v", err)
	}
}
