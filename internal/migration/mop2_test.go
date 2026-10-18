package migration

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// This battery drives the remaining fault branches the other suites do not
// reach: state-file durability seams, dry-run/managed prerequisites, restore
// layouts whose blocking conditions are layout-shaped, walk classification
// tails, and the reviewed-installation success row through a local shim.

// ---------------------------------------------------------------------------
// Keys and small guards

func TestReadKeyShapes(t *testing.T) {
	if _, err := ReadKey(filepath.Join(t.TempDir(), "missing.key")); err == nil {
		t.Fatal("ReadKey accepted a missing key file")
	}
	short := filepath.Join(t.TempDir(), "short.key")
	if err := os.WriteFile(short, []byte("too-short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadKey(short); err == nil {
		t.Fatal("ReadKey accepted a short key")
	}
	long := filepath.Join(t.TempDir(), "long.key")
	if err := os.WriteFile(long, make([]byte, KeySize+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadKey(long); err == nil {
		t.Fatal("ReadKey accepted an oversized key")
	}
	keyPath := filepath.Join(t.TempDir(), "ok.key")
	if err := os.WriteFile(keyPath, bytes.Repeat([]byte{7}, KeySize), 0o600); err != nil {
		t.Fatal(err)
	}
	if key, err := ReadKey(keyPath); err != nil || len(key) != KeySize {
		t.Fatalf("valid key rejected: %d %v", len(key), err)
	}
	// CaptureWithOptions validates the key length itself.
	source := t.TempDir()
	if _, err := CaptureWithOptions(source, t.TempDir(), []byte("short"), false, false, nil); err == nil {
		t.Fatal("capture accepted a short key")
	}
}

func TestUserConfigDirEnvOverride(t *testing.T) {
	override := t.TempDir()
	t.Setenv("MIGRATE_USER_CONFIG_DIR", override)
	got, err := resolveUserConfigDir()
	if err != nil || got != override {
		t.Fatalf("MIGRATE_USER_CONFIG_DIR override lost: %q %v", got, err)
	}
}

func TestCaptureGitSettingUnreadablePath(t *testing.T) {
	// A NUL byte makes the path unopenable on every platform with an error
	// that is not not-exist, which is the branch the OS otherwise hides.
	if _, err := CaptureGitSetting("home\x00bad"); err == nil {
		t.Fatal("CaptureGitSetting accepted an unopenable path")
	}
}

func TestChromeExtensionIDsUnreadablePath(t *testing.T) {
	if _, err := chromeExtensionIDs("dir\x00bad"); err == nil {
		t.Fatal("chromeExtensionIDs accepted an unperformable ReadDir")
	}
}

// ---------------------------------------------------------------------------
// State file durability

func TestNewRestoreStateDefaultsAndFault(t *testing.T) {
	// Version normalisation and the items map default.
	state, err := NewRestoreState(Manifest{Version: 0}, filepath.Join(t.TempDir(), "dst"), "personal", false)
	if err != nil {
		t.Fatal(err)
	}
	if state.Version != 1 || state.Items == nil || state.DryRunCompleted {
		t.Fatalf("zero-value state defaults wrong: %#v", state)
	}
	// An unresolvable destination fails the plan fingerprint.
	overrideAbsPath(t, failingAbsPath())
	if _, err := NewRestoreState(plainManifest(), "dst", "personal", false); err == nil {
		t.Fatal("NewRestoreState accepted an unresolvable destination")
	}
}

func TestSaveRestoreStateFaults(t *testing.T) {
	repository := t.TempDir()
	key := bytes.Repeat([]byte{9}, KeySize)
	state := RestoreState{
		Version:           1,
		BundleFingerprint: "bundle",
		PlanFingerprint:   "plan",
		Destination:       "dst",
		DryRunCompleted:   true,
	}
	realChmodFn, realWriteFn, realSyncFn, realCloseFn := fileChmod, fileWrite, fileSync, fileClose

	// chmod fault: the temporary is closed and the error is forwarded.
	savedChmod := fileChmod
	seamOverride(t, &fileChmod, func(*os.File, os.FileMode) error { return errors.New("state chmod refused") })
	if err := SaveRestoreState(repository, key, state); err == nil {
		t.Fatal("chmod fault swallowed")
	}
	fileChmod = savedChmod

	// write fault.
	savedWrite := fileWrite
	seamOverride(t, &fileWrite, func(*os.File, []byte) (int, error) { return 0, errors.New("state write refused") })
	if err := SaveRestoreState(repository, key, state); err == nil {
		t.Fatal("write fault swallowed")
	}
	fileWrite = savedWrite

	// sync fault.
	savedSync := fileSync
	seamOverride(t, &fileSync, func(*os.File) error { return errors.New("state sync refused") })
	if err := SaveRestoreState(repository, key, state); err == nil {
		t.Fatal("sync fault swallowed")
	}
	fileSync = savedSync

	// close fault: failingClose releases the handle before reporting.
	savedClose := fileClose
	seamOverride(t, &fileClose, failingClose())
	if err := SaveRestoreState(repository, key, state); err == nil {
		t.Fatal("close fault swallowed")
	}
	fileClose = savedClose

	// temporary-creation fault: the rename is never reached.
	savedTemp := osCreateTemp
	seamOverride(t, &osCreateTemp, failingCreateTemp())
	if err := SaveRestoreState(repository, key, state); err == nil {
		t.Fatal("temp fault swallowed")
	}
	osCreateTemp = savedTemp

	// No fault remains: the state file persists and reloads.
	if err := SaveRestoreState(repository, key, state); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadRestoreState(repository, key)
	if err != nil || !reloaded.DryRunCompleted || reloaded.BundleFingerprint != "bundle" || reloaded.PlanFingerprint != "plan" {
		t.Fatalf("state round trip: %#v %v", reloaded, err)
	}
	_ = realChmodFn
	_ = realWriteFn
	_ = realSyncFn
	_ = realCloseFn
}

// ---------------------------------------------------------------------------
// Dry-run / managed prerequisites

// rewireManifest rewrites an existing repository's manifest through an
// arbitrary editing callback, sealing the result with the repository's key.
func rewireManifest(t *testing.T, repository string, key []byte, mutate func(manifest *Manifest)) {
	t.Helper()
	manifest, err := LoadManifest(repository, key)
	if err != nil {
		t.Fatal(err)
	}
	mutate(&manifest)
	encoded, _ := json.Marshal(manifest)
	ciphertext, _ := encryptBytes(key, encoded, []byte("manifest-v1"))
	if err := os.WriteFile(filepath.Join(repository, manifestFile), ciphertext, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestDryRunSequences drives the dry-run wrapper's own error branches with a
// zero-item manifest so only the wrapper's own resolver calls are in play
// (PlanRestore resolves the destination exactly once; the second resolve is
// the state fingerprint).
func TestDryRunSequences(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "src")
	repository := filepath.Join(root, "repo")
	destination := filepath.Join(root, "dst")
	keyPath := filepath.Join(root, "key")
	key := bytes.Repeat([]byte{71}, KeySize)
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Capture(source, repository, key); err != nil {
		t.Fatal(err)
	}
	if _, err := DryRunRestore(repository, destination, keyPath, "personal", false); err != nil {
		t.Fatal(err)
	}
	calls := 0
	overrideAbsPath(t, func(path string) (string, error) {
		calls++
		if calls == 2 {
			return "", errors.New("destination resolution refused")
		}
		return filepath.Abs(path)
	})
	if _, err := DryRunRestore(repository, destination, keyPath, "personal", false); err == nil {
		t.Fatal("dry-run accepted an unresolvable destination on the state path")
	}
	overrideAbsPath(t, filepath.Abs)

	// A state-save fault surfaces through the dry-run wrapper itself.
	savedChmod := fileChmod
	seamOverride(t, &fileChmod, func(*os.File, os.FileMode) error { return errors.New("state chmod refused") })
	if _, err := DryRunRestore(repository, destination, keyPath, "personal", false); err == nil {
		t.Fatal("dry-run sealed its state despite a failing chmod")
	}
	fileChmod = savedChmod

	// The dry-run still finishes and records its state afterwards.
	if _, err := DryRunRestore(repository, destination, keyPath, "personal", false); err != nil {
		t.Fatal(err)
	}
	// An unreadable manifest is the last prerequisite fault.
	if err := os.WriteFile(filepath.Join(repository, manifestFile), sealedPlain(key, []byte("garbage-not-json")), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ManagedRestore(repository, destination, keyPath, "personal", false, false); err == nil {
		t.Fatal("ManagedRestore accepted an unreadable manifest")
	}
}

func TestManagedRestoreFingerprintSecondResolve(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "src")
	repository := filepath.Join(root, "repo")
	destination := filepath.Join(root, "dst")
	keyPath := filepath.Join(root, "key")
	key := bytes.Repeat([]byte{72}, KeySize)
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Capture(source, repository, key); err != nil {
		t.Fatal(err)
	}
	// Zero items keep PlanRestore to exactly one resolver call.
	rewireManifest(t, repository, key, func(manifest *Manifest) { manifest.Items = []Item{} })
	if _, err := DryRunRestore(repository, destination, keyPath, "personal", false); err != nil {
		t.Fatal(err)
	}
	calls := 0
	overrideAbsPath(t, func(path string) (string, error) {
		calls++
		if calls == 2 {
			return "", errors.New("second resolution refused")
		}
		return filepath.Abs(path)
	})
	if _, err := ManagedRestore(repository, destination, keyPath, "personal", false, false); err == nil {
		t.Fatal("restore accepted an unresolvable plan fingerprint")
	}
}

func TestManagedRestoreWorkspaceFault(t *testing.T) {
	repository, _, keyPath := firstCapturedRepository(t)
	key := readKeyOrFatal(t, keyPath)
	rewireManifest(t, repository, key, func(manifest *Manifest) {
		manifest.SourceOS = "windows"
		manifest.Applications = []Application{{Manager: "winget", Identifier: "Git.Git"}}
	})
	// Keep every recipe targeted at sealed test roots, never the operator's
	// own configuration directory.
	setUserConfigRoot(t, t.TempDir())
	// The install workspace is created for same-OS plans only; pin the
	// restore platform so the seam fires on every host, and keep the pin
	// across the dry-run because the workspace plan rows join the
	// fingerprint.
	pinPlatform := overridePlatform("windows")
	t.Cleanup(pinPlatform)
	destination := t.TempDir()
	if _, err := DryRunRestore(repository, destination, keyPath, "personal", false); err != nil {
		t.Fatal(err)
	}
	// Nothing on PATH: any extension/CLI guard degrades to records instead
	// of attempting real installs.
	emptyLookPath := overrideLookPath([]string{})
	t.Cleanup(emptyLookPath)
	seamOverride(t, &osMkdirTemp, func(string, string) (string, error) { return "", errors.New("no workspaces") })
	report, err := ManagedRestore(repository, destination, keyPath, "personal", false, true)
	if err == nil || !strings.Contains(err.Error(), "no workspaces") {
		t.Fatalf("workspace fault not surfaced: %v — rows: %#v", err, report.Results)
	}
}

// ---------------------------------------------------------------------------
// RestoreWithOptions battery

func TestRestoreWithOptionsBattery(t *testing.T) {
	repository, _, keyPath := firstCapturedRepository(t)
	key := readKeyOrFatal(t, keyPath)
	// Missing repository.
	if err := RestoreWithOptions(filepath.Join(t.TempDir(), "missing"), t.TempDir(), key, false, false, false, nil); err == nil {
		t.Fatal("RestoreWithOptions accepted a missing repository")
	}
	// An unresolvable destination fails the plan.
	overrideAbsPath(t, failingAbsPath())
	if err := RestoreWithOptions(repository, t.TempDir(), key, false, false, false, nil); err == nil {
		t.Fatal("RestoreWithOptions accepted an unresolvable destination")
	}
	overrideAbsPath(t, filepath.Abs)
	// pure dry-run: plans are built, nothing is restored.
	if err := RestoreWithOptions(repository, t.TempDir(), key, false, true, false, nil); err != nil {
		t.Fatalf("dry-run restore must be a no-op: %v", err)
	}
	// Destination whose parent is a file: mkdir fails.
	root := t.TempDir()
	blocked := filepath.Join(root, "blocked")
	if err := os.WriteFile(blocked, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RestoreWithOptions(repository, filepath.Join(blocked, "sub", "dst"), key, false, false, false, nil); err == nil {
		t.Fatal("restore surfaced no mkdir failure")
	}
	// Chmod fails through the seam after a successful decrypt.
	unblocked := filepath.Join(t.TempDir(), "dst")
	savedChmod := osChmod
	seamOverride(t, &osChmod, func(string, os.FileMode) error { return errors.New("chmod refused") })
	if err := RestoreWithOptions(repository, unblocked, key, false, false, false, nil); err == nil {
		t.Fatal("restore surfaced no chmod failure")
	}
	osChmod = savedChmod
	// Install flow with a nil runner falls back to the system runner; the
	// winget guard fires before anything executes.
	rewireManifest(t, repository, key, func(manifest *Manifest) {
		manifest.Applications = []Application{{Manager: "winget", Identifier: "Git.Git"}}
	})
	emptyLookPath := overrideLookPath([]string{})
	t.Cleanup(emptyLookPath)
	if err := RestoreWithOptions(repository, unblocked, key, false, false, true, nil); err == nil {
		t.Fatal("restore surfaced no winget failure")
	}
}

// ---------------------------------------------------------------------------
// Manifest lifecycle guards

func TestCaptureManifestWriteFault(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	repository := t.TempDir()
	// A directory at the manifest path blocks the final write.
	if err := os.MkdirAll(filepath.Join(repository, manifestFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Capture(source, repository, bytes.Repeat([]byte{75}, KeySize)); err == nil {
		t.Fatal("capture accepted an unwritable manifest")
	}
}

func TestLoadManifestShortKey(t *testing.T) {
	repository := t.TempDir()
	if err := os.WriteFile(filepath.Join(repository, manifestFile), []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifest(repository, bytes.Repeat([]byte{2}, KeySize-1)); err == nil {
		t.Fatal("LoadManifest accepted a short key")
	}
	if _, err := LoadManifest(repository, bytes.Repeat([]byte{2}, KeySize+1)); err == nil {
		t.Fatal("LoadManifest accepted an oversized key")
	}
}

func TestRestoreWithOptionsCorruptObject(t *testing.T) {
	repository, _, keyPath := firstCapturedRepository(t)
	key := readKeyOrFatal(t, keyPath)
	manifest, err := LoadManifest(repository, key)
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt the first object's ciphertext: restore must surface it, not
	// write a half-decrypted target.
	objectPath := filepath.Join(repository, objectDir, manifest.Items[0].ID+".bin")
	raw, err := os.ReadFile(objectPath)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0xFF
	if err := os.WriteFile(objectPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RestoreWithOptions(repository, filepath.Join(t.TempDir(), "dst"), key, false, false, false, nil); err == nil {
		t.Fatal("restore surfaced no decrypt failure")
	}
}

func TestReuseOrEncryptReencryptFault(t *testing.T) {
	key := bytes.Repeat([]byte{76}, KeySize)
	root := t.TempDir()
	source := filepath.Join(root, "source.txt")
	if err := os.WriteFile(source, []byte("fresh content"), 0o644); err != nil {
		t.Fatal(err)
	}
	item := Item{ID: strings.Repeat("cd", 32), SHA256: mustFileHash(t, source)}
	objectPath := filepath.Join(root, "tampered.bin")
	if err := os.WriteFile(objectPath, []byte("garbage ciphertext"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The existing object fails verification, so the re-encryption runs —
	// and its temporary-creation seam is faulted.
	faultTemp := failingCreateTemp()
	seamOverride(t, &osCreateTemp, faultTemp)
	if err := reuseOrEncryptFile(source, objectPath, key, item); err == nil {
		t.Fatal("reuse accepted a failing re-encryption")
	}
}

// ---------------------------------------------------------------------------
// Managed restore with failing settings step

func TestManagedRestoreSettingsFault(t *testing.T) {
	repository, destination, keyPath := firstCapturedRepository(t)
	key := readKeyOrFatal(t, keyPath)
	rewireManifest(t, repository, key, func(manifest *Manifest) {
		manifest.Checklist = nil
		manifest.Settings = []Setting{{Recipe: "git-v1", Values: map[string]string{"name": "n", "email": "e"}}}
	})
	// Keep recipes sealed in the test root.
	setUserConfigRoot(t, t.TempDir())
	// The dry-run runs first (with the same overwrite flag the restore will
	// use, because the flag is part of the plan fingerprint), then the
	// destination gains a directory where .gitconfig should be written: the
	// settings step fails and the report still carries every completed row.
	if _, err := DryRunRestore(repository, destination, keyPath, "personal", true); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(destination, ".gitconfig"), 0o755); err != nil {
		t.Fatal(err)
	}
	report, err := ManagedRestore(repository, destination, keyPath, "personal", true, false)
	if err == nil {
		t.Fatal("restore surfaced no settings failure")
	}
	if !verifyHasStatus(report.Results, "copied") {
		t.Fatalf("file copies missing from failed-settings report: %#v", report.Results)
	}
}

// ---------------------------------------------------------------------------
// Capture with a denied Chrome Extensions directory

func TestCaptureChromeExtensionsDenied(t *testing.T) {
	home := t.TempDir()
	defaults := filepath.Join(chromeUserDataDirForOS(runtime.GOOS, home), "Default")
	// A readable Bookmarks file gets past the first guard.
	if err := os.MkdirAll(defaults, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(defaults, "Bookmarks"), []byte(`{"roots":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	extensions := filepath.Join(defaults, "Extensions")
	if err := os.MkdirAll(extensions, 0o755); err != nil {
		t.Fatal(err)
	}
	var deny, restore *exec.Cmd
	if runtime.GOOS == "windows" {
		deny = exec.Command("icacls", extensions, "/deny", "*S-1-1-0:(OI)(CI)F")
		// The window owner keeps implicit WRITE_DAC, so removing the deny
		// ACE always succeeds even though reads stay blocked.
		restore = exec.Command("icacls", extensions, "/remove:d", "*S-1-1-0")
	} else {
		deny = exec.Command("chmod", "000", extensions)
		restore = exec.Command("chmod", "755", extensions)
	}
	if out, err := deny.CombinedOutput(); err != nil {
		t.Fatalf("deny step failed: %v — %s", err, out)
	}
	t.Cleanup(func() {
		_ = restore.Run()
	})
	if _, _, err := captureChromeSetting(home); err == nil {
		t.Fatal("a denied Extensions directory must fail the capture")
	}
}

// ---------------------------------------------------------------------------
// Render guards and restore layouts

func TestRenderVSCodeSettingGuards(t *testing.T) {
	if _, _, err := RenderVSCodeSetting(Setting{Recipe: "vscode-v2"}); err == nil {
		t.Fatal("foreign VS Code recipe accepted")
	}
	if _, _, err := RenderVSCodeSetting(Setting{Recipe: "vscode-v1", Values: map[string]string{"extensions": "a"}}); err == nil {
		t.Fatal("recipe without settings.json accepted")
	}
	if _, _, err := RenderVSCodeSetting(Setting{Recipe: "vscode-v1", Values: map[string]string{"settings.json": "  "}}); err == nil {
		t.Fatal("whitespace-only settings accepted")
	}
	if _, _, err := RenderVSCodeSetting(Setting{Recipe: "vscode-v1", Values: map[string]string{
		"settings.json": "{}", "evil": "1",
	}}); err == nil {
		t.Fatal("unsupported value key accepted")
	}
	if _, _, err := RenderVSCodeSetting(Setting{Recipe: "vscode-v1", Values: map[string]string{"settings.json": "{}\x00"}}); err == nil {
		t.Fatal("NUL byte accepted in settings")
	}
	content, extensions, err := RenderVSCodeSetting(Setting{Recipe: "vscode-v1", Values: map[string]string{
		"settings.json": "{}", "extensions": " b\na\nb\n",
	}})
	if err != nil || string(content) != "{}" || len(extensions) != 3 || extensions[0] != "b" {
		t.Fatalf("clean VS Code recipe: %#v %#v %v", content, extensions, err)
	}
}

func chromeDefaultDir(t *testing.T, destination string) string {
	t.Helper()
	return filepath.Join(chromeUserDataDirForOS(runtime.GOOS, destination), "Default")
}

func TestRestoreChromeLayoutBranches(t *testing.T) {
	setting := Setting{Recipe: "chrome-v1", Values: map[string]string{chromeBookmarksFile: `{"roots":{}}`}}
	// skipped-existing needs the file present and no --overwrite.
	skip := t.TempDir()
	defaults := chromeDefaultDir(t, skip)
	if err := os.MkdirAll(defaults, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(defaults, "Bookmarks"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := restoreManually(t, []Setting{setting}, skip, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reportHasStatusKind(report.Results, "setting", "skipped-existing") {
		t.Fatalf("chrome skipped-existing row missing: %#v", report.Results)
	}
	// mkdir failure: the Default directory is a file.
	blocked := t.TempDir()
	blockedDefault := chromeDefaultDir(t, blocked)
	if err := os.MkdirAll(filepath.Dir(blockedDefault), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blockedDefault, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := restoreManually(t, []Setting{setting}, blocked, true); err == nil {
		t.Fatal("chrome restore surfaced no mkdir failure")
	}
	// write failure: Bookmarks itself is a directory and --overwrite is on.
	writable := t.TempDir()
	writableDefault := chromeDefaultDir(t, writable)
	if err := os.MkdirAll(filepath.Join(writableDefault, "Bookmarks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := restoreManually(t, []Setting{setting}, writable, true); err == nil {
		t.Fatal("chrome restore surfaced no write failure")
	}
	// A recipe whose palette carries an unsupported value renders as an
	// unsupported row.
	badRender := Setting{Recipe: "chrome-v1", Values: map[string]string{"evil": "1"}}
	report, err = restoreManually(t, []Setting{badRender}, t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	if !reportHasStatusKind(report.Results, "setting", "unsupported") {
		t.Fatalf("chrome unsupported row missing: %#v", report.Results)
	}
}

func TestRestoreOpenCodeLayoutBranches(t *testing.T) {
	setting := Setting{Recipe: "opencode-v1", Values: map[string]string{
		openCodeConfigFile: `{"model":"opus"}`,
		openCodeAuthFile:   `{"anthropic":"key"}`,
	}}
	// skipped-existing for both documents.
	skip := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(openCodeConfigPath(skip)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(openCodeConfigPath(skip), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(openCodeAuthPath(skip)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(openCodeAuthPath(skip), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := restoreManually(t, []Setting{setting}, skip, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reportHasStatusKind(report.Results, "credential", "skipped-existing") {
		t.Fatalf("opencode auth skipped row missing: %#v", report.Results)
	}
	// mkdir failure for the config: its directory is a file.
	blocked := t.TempDir()
	blockedDir := filepath.Dir(openCodeConfigPath(blocked))
	if err := os.MkdirAll(filepath.Dir(blockedDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blockedDir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := restoreManually(t, []Setting{setting}, blocked, true); err == nil {
		t.Fatal("opencode restore surfaced no config mkdir failure")
	}
	// write failure for the config: opencode.json is a directory.
	writable := t.TempDir()
	if err := os.MkdirAll(openCodeConfigPath(writable), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := restoreManually(t, []Setting{setting}, writable, true); err == nil {
		t.Fatal("opencode restore surfaced no config write failure")
	}
	// auth mkdir failure with a working config first.
	authMkdirBlocked := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(openCodeConfigPath(authMkdirBlocked)), 0o755); err != nil {
		t.Fatal(err)
	}
	blockedAuthDir := filepath.Dir(openCodeAuthPath(authMkdirBlocked))
	if err := os.MkdirAll(filepath.Dir(blockedAuthDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blockedAuthDir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := restoreManually(t, []Setting{setting}, authMkdirBlocked, true); err == nil {
		t.Fatal("opencode restore surfaced no auth mkdir failure")
	}
	// auth write failure: auth.json is a directory.
	authWriteBlocked := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(openCodeConfigPath(authWriteBlocked)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(openCodeAuthPath(authWriteBlocked), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := restoreManually(t, []Setting{setting}, authWriteBlocked, true); err == nil {
		t.Fatal("opencode restore surfaced no auth write failure")
	}
	// A recipe with an unsupported value renders as an unsupported row.
	badRender := Setting{Recipe: "opencode-v1", Values: map[string]string{"evil": "1"}}
	report, err = restoreManually(t, []Setting{badRender}, t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	if !reportHasStatusKind(report.Results, "setting", "unsupported") {
		t.Fatalf("opencode unsupported row missing: %#v", report.Results)
	}
}

func TestRestoreCatalogLayoutFaults(t *testing.T) {
	docker := Setting{Recipe: "docker-v1", Values: map[string]string{"docker-config": `{"credsStore":"desktop"}`}}
	// mkdir failure: destination/.docker is a file.
	blocked := t.TempDir()
	if err := os.WriteFile(filepath.Join(blocked, ".docker"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := restoreManually(t, []Setting{docker}, blocked, true); err == nil {
		t.Fatal("docker restore surfaced no mkdir failure")
	}
	// write failure: config.json is a directory.
	writable := t.TempDir()
	if err := os.MkdirAll(filepath.Join(writable, ".docker", "config.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := restoreManually(t, []Setting{docker}, writable, true); err == nil {
		t.Fatal("docker restore surfaced no write failure")
	}
}

func TestRestoreChecklistLayouts(t *testing.T) {
	checklistManifest := plainManifestWith(nil)
	checklistManifest.Checklist = []string{"Re-scan the bundle before decommissioning the old laptop", "Sign back in to Chrome"}
	restore := func(destination string, overwrite bool) (Report, error) {
		report := NewReport("restore")
		err := restoreSettings(checklistManifest, destination, &report, overwrite, false, nil)
		return report, err
	}
	// skipped-existing.
	skip := t.TempDir()
	if err := os.WriteFile(filepath.Join(skip, "FINISH-SETUP.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := restore(skip, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reportHasStatusKind(report.Results, "checklist", "skipped-existing") {
		t.Fatalf("checklist skipped row missing: %#v", report.Results)
	}
	// restored.
	full := t.TempDir()
	report, err = restore(full, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reportHasStatusKind(report.Results, "checklist", "restored") {
		t.Fatalf("checklist restored row missing: %#v", report.Results)
	}
	content, err := os.ReadFile(filepath.Join(full, "FINISH-SETUP.txt"))
	if err != nil || !strings.Contains(string(content), "2. Sign back in to Chrome") {
		t.Fatalf("checklist content wrong: %s %v", content, err)
	}
	// A directory at FINISH-SETUP.txt breaks the write.
	dirBlocked := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dirBlocked, "FINISH-SETUP.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := restore(dirBlocked, true); err == nil {
		t.Fatal("checklist restore surfaced no write failure")
	}
}

func TestCaptureDevSettingsUnreadable(t *testing.T) {
	home := t.TempDir()
	// The docker file cannot be read: it is a directory.
	if err := os.MkdirAll(filepath.Join(home, ".docker", "config.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := captureDevSettings(home); err == nil {
		t.Fatal("captureDevSettings accepted an unreadable docker file")
	}
	// The same fault surfaces as a "devenv" exclusion during a full capture,
	// never as a silent empty dev section.
	repository := t.TempDir()
	key := bytes.Repeat([]byte{73}, KeySize)
	manifest, err := CaptureWithOptions(home, repository, key, false, false, stubRunner(nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, exclusion := range manifest.Exclusions {
		if strings.Contains(exclusion.Reason, "setting capture unavailable") {
			found = true
		}
	}
	if !found {
		t.Fatalf("dev exclusion missing: %#v", manifest.Exclusions)
	}
}

// ---------------------------------------------------------------------------
// Plan guards and walk tails

func TestPlanRestoreRefusesBlockedTarget(t *testing.T) {
	// A target the OS cannot inspect at all (not merely missing) is refused
	// through the symlink/reparse guard's error forwarding.
	reload := func() {
		t.Helper()
		if runtime.GOOS == "windows" {
			// "ba|d.txt" is an invalid DOS name: Lstat reports a non-not-exist error.
			item := Item{ID: strings.Repeat("ab", 32), RelativePath: "ba|d.txt", SHA256: strings.Repeat("cd", 32)}
			if _, err := PlanRestore(Manifest{Items: []Item{item}}, t.TempDir(), false); err == nil {
				t.Fatal("PlanRestore accepted an uninspectable target")
			}
			return
		}
		// A parent directory with no execute/search bit blocks the lookup.
		root := t.TempDir()
		shut := filepath.Join(root, "shut")
		if err := os.MkdirAll(shut, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(shut, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(shut, 0o755) })
		item := Item{ID: strings.Repeat("ab", 32), RelativePath: "shut/a.txt", SHA256: strings.Repeat("cd", 32)}
		if _, err := PlanRestore(Manifest{Items: []Item{item}}, root, false); err == nil {
			t.Fatal("PlanRestore accepted an uninspectable target")
		}
	}
	reload()
}

func TestClassifyWalkEntryReadFailure(t *testing.T) {
	root := t.TempDir()
	repository := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "plain.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A regular entry whose file vanished between listing and hashing lands
	// in the unreadable exclusion. The NUL byte guarantees the open fails on
	// every platform without touching a real file.
	entry := fakeDirEntry{name: "plain.txt", info: fakeFileInfo{name: "plain.txt", size: 1, mode: fs.FileMode(0o644)}}
	outcome, err := classifyWalkEntry(root, repository, false, filepath.Join(root, "plain.txt")+"\x00", entry, nil)
	if err != nil || outcome.exclusion == nil || outcome.exclusion.Reason != "unreadable" {
		t.Fatalf("unreadable file: %#v %v", outcome.exclusion, err)
	}
}

// ---------------------------------------------------------------------------
// Wire-format tails

func TestDecodeAndVerifyMidStreamFaults(t *testing.T) {
	key := bytes.Repeat([]byte{74}, KeySize)
	root := t.TempDir()
	source := filepath.Join(root, "plain.txt")
	if err := os.WriteFile(source, []byte("tail"), 0o644); err != nil {
		t.Fatal(err)
	}
	objectPath := filepath.Join(root, "tail.bin")
	if err := encryptFile(source, objectPath, key, "tail"); err != nil {
		t.Fatal(err)
	}
	// Truncate the final chunk's payload: the frame header survives, the
	// payload does not. Both the streaming decoder and the verifier report it.
	raw, err := os.ReadFile(objectPath)
	if err != nil {
		t.Fatal(err)
	}
	truncated := append([]byte(nil), raw[:len(raw)-8]...)
	if err := os.WriteFile(filepath.Join(root, "cut.bin"), truncated, 0o600); err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(filepath.Join(root, "cut.bin"))
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if _, _, err := decodeObjectStream(input, &bytes.Buffer{}, key, "tail"); err == nil {
		t.Fatal("decoder accepted a truncated tail")
	}
	if _, _, err := verifyFile(filepath.Join(root, "cut.bin"), key, "tail"); err == nil {
		t.Fatal("verifier accepted a truncated tail")
	}
	// A two-byte magic opens the verify ladder.
	stub := filepath.Join(root, "stub.bin")
	if err := os.WriteFile(stub, []byte("MK"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := verifyFile(stub, key, "tail"); err == nil {
		t.Fatal("verifier accepted a truncated magic")
	}
	if _, _, err := verifyFile(filepath.Join(root, "missing.bin"), key, "tail"); err == nil {
		t.Fatal("verifier accepted a missing object")
	}
}

// ---------------------------------------------------------------------------
// Reviewed install success row through a local shim (never a real manager)

func installShimPath(t *testing.T) string {
	t.Helper()
	shim := t.TempDir()
	name := "brew"
	if runtime.GOOS == "windows" {
		if err := os.WriteFile(filepath.Join(shim, name+".cmd"), []byte("@echo off\r\nexit /b 0\r\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := os.WriteFile(filepath.Join(shim, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return shim
}

func TestManagedInstallShimmedSuccessRow(t *testing.T) {
	repository, _, keyPath := firstCapturedRepository(t)
	key := readKeyOrFatal(t, keyPath)
	rewireManifest(t, repository, key, func(manifest *Manifest) {
		// Mac captures record Homebrew managers as "brew-brew"/"brew-cask"
		// (ParseBrewfile), so replay the real capture shape here.
		manifest.SourceOS = "darwin"
		manifest.Applications = []Application{{Manager: "brew-brew", Identifier: "git"}}
	})
	destination := t.TempDir()
	if _, err := DryRunRestore(repository, destination, keyPath, "personal", false); err != nil {
		t.Fatal(err)
	}
	// The shim directory is the only PATH entry: the "package manager" the
	// system command runner resolves is our reviewed exit-0 stub, which is
	// exactly the darwin flow rehearsed without physical hardware.
	t.Setenv("PATH", installShimPath(t))
	if runtime.GOOS == "windows" {
		t.Setenv("PATHEXT", ".cmd")
	}
	t.Cleanup(overridePlatform("darwin"))
	report, err := ManagedRestore(repository, destination, keyPath, "personal", false, true)
	if err != nil {
		t.Fatalf("shimmed install surfaced an error: %v — report: %#v", err, report.Results)
	}
	if !reportHasStatusKind(report.Results, "application", "installed") {
		t.Fatalf("shimmed install row missing: %#v", report.Results)
	}
}

func TestAllManagerAndErrorString(t *testing.T) {
	if !allManager([]Application{{Manager: "winget"}, {Manager: "winget"}}, "winget") {
		t.Fatal("uniform managers misclassified")
	}
	if allManager([]Application{{Manager: "brew"}}, "winget") {
		t.Fatal("mixed managers misclassified")
	}
	if errorString(nil) != "" {
		t.Fatal("nil error rendered")
	}
	if errorString(errors.New("boom")) != "boom" {
		t.Fatal("real error rendered badly")
	}
}
