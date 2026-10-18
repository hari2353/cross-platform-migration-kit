package migration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Seam plumbing: one generic override helper keeps each fault test terse.

func seamOverride[T any](t *testing.T, slot *T, value T) {
	t.Helper()
	saved := *slot
	*slot = value
	t.Cleanup(func() { *slot = saved })
}

func overrideAbsPath(t *testing.T, fn func(string) (string, error)) {
	seamOverride(t, &absPath, fn)
}

func failingAbsPath() func(string) (string, error) {
	return func(string) (string, error) { return "", errors.New("no absolute path available") }
}

func failingCreateTemp() func(string, string) (*os.File, error) {
	return func(string, string) (*os.File, error) { return nil, errors.New("no temporaries on this disk") }
}

func fileCloseReal() func(*os.File) error {
	return func(f *os.File) error { return f.Close() }
}

func fileWriteReal() func(*os.File, []byte) (int, error) {
	return func(f *os.File, p []byte) (int, error) { return f.Write(p) }
}

// ---------------------------------------------------------------------------
// Shared fixtures.

func plainManifest() Manifest {
	return Manifest{
		Version:    1,
		SourceOS:   runtime.GOOS,
		Items:      []Item{{ID: strings.Repeat("ab", 32), RelativePath: "a.txt", SHA256: strings.Repeat("cd", 32)}},
		SourceRoot: "src",
	}
}

func plainManifestWith(settings []Setting) Manifest {
	manifest := plainManifest()
	manifest.Settings = settings
	return manifest
}

func mustFileHash(t *testing.T, path string) string {
	t.Helper()
	hash, err := fileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func writeTempBytes(t *testing.T, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bytes.bin")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// First-captured variant that returns the repository path alone (for pack
// and launcher fault tests that mutate the repository after capture).
func firstCapturedRepositoryDir(t *testing.T) string {
	t.Helper()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	repository := t.TempDir()
	if _, err := Capture(source, repository, bytes.Repeat([]byte{53}, KeySize)); err != nil {
		t.Fatal(err)
	}
	return repository
}

// addApplicationToManifest seals an application list (and optionally a
// source OS) into an existing repository manifest so restore flows can
// exercise the install paths.
func addApplicationToManifest(t *testing.T, repository string, key []byte, sourceOS string, applications []Application) {
	t.Helper()
	manifest, err := LoadManifest(repository, key)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Applications = applications
	if sourceOS != "" {
		manifest.SourceOS = sourceOS
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := encryptBytes(key, encoded, []byte("manifest-v1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, manifestFile), ciphertext, 0o600); err != nil {
		t.Fatal(err)
	}
}

func reportHasStatusKind(results []Result, kind, status string) bool {
	for _, result := range results {
		if result.Kind == kind && result.Status == status {
			return true
		}
	}
	return false
}

func setUserConfigRoot(t *testing.T, root string) {
	t.Helper()
	seamOverride(t, &userConfigDirFunc, func() (string, error) { return root, nil })
}

// ---------------------------------------------------------------------------

func TestAbsSeamBranches(t *testing.T) {
	repository, destination, keyPath := firstCapturedRepository(t)

	// DryRunRestore resolves the destination through the seam inside
	// PlanRestore.
	overrideAbsPath(t, failingAbsPath())
	if _, err := DryRunRestore(repository, destination, keyPath, "personal", false); err == nil {
		t.Fatal("DryRunRestore accepted a failing path resolver")
	}
	// PlanRestore directly.
	if _, err := PlanRestore(plainManifest(), t.TempDir(), false); err == nil {
		t.Fatal("PlanRestore accepted a failing path resolver")
	}
	// isWithin must fail closed: the candidate branch first.
	if isWithin(t.TempDir(), t.TempDir()) {
		t.Fatal("isWithin must fail closed when the candidate cannot be resolved")
	}
	// isWithin root branch: the second resolver invocation fails.
	calls := 0
	root, target := t.TempDir(), t.TempDir()
	overrideAbsPath(t, func(path string) (string, error) {
		calls++
		if calls == 2 {
			return "", errors.New("root resolver failure")
		}
		return filepath.Abs(path)
	})
	if isWithin(target, root) {
		t.Fatal("isWithin must fail closed when the root cannot be resolved")
	}
	// rejectSymlinkedParents(target, root): the root resolver fails first.
	overrideAbsPath(t, failingAbsPath())
	if err := rejectSymlinkedParents(target, root); err == nil {
		t.Fatal("rejectSymlinkedParents accepted a failing root resolver")
	}
	// rejectSymlinkedParents target branch: the second invocation (target) fails.
	calls = 0
	overrideAbsPath(t, func(path string) (string, error) {
		calls++
		if calls == 2 {
			return "", errors.New("target resolver failure")
		}
		return filepath.Abs(path)
	})
	if err := rejectSymlinkedParents(target, root); err == nil {
		t.Fatal("rejectSymlinkedParents accepted a failing target resolver")
	}
	// isWithin across volumes: Rel cannot express the difference.
	if runtime.GOOS == "windows" && isWithin(`C:\Windows`, `D:\`) {
		t.Fatal("a C: candidate must never sit inside a D: root")
	}
}

func TestPlanFingerprintAbsBranch(t *testing.T) {
	overrideAbsPath(t, failingAbsPath())
	if _, err := PlanFingerprint(plainManifest(), "dst", "personal", false); err == nil {
		t.Fatal("PlanFingerprint accepted a failing path resolver")
	}
}

func TestCaptureWithOptionsAbsBranches(t *testing.T) {
	key := bytes.Repeat([]byte{29}, KeySize)
	source := t.TempDir()
	// The source resolver fails first.
	overrideAbsPath(t, failingAbsPath())
	if _, err := CaptureWithOptions(source, t.TempDir(), key, false, false, nil); err == nil {
		t.Fatal("capture accepted a failing source resolver")
	}
	// The repository resolver fails second: only repository paths fail.
	overrideAbsPath(t, func(path string) (string, error) {
		if strings.HasSuffix(path, "repo") {
			return "", errors.New("repository resolver failure")
		}
		return filepath.Abs(path)
	})
	if _, err := CaptureWithOptions(source, filepath.Join(t.TempDir(), "repo"), key, false, false, nil); err == nil {
		t.Fatal("capture accepted a failing repository resolver")
	}
}

// ---------------------------------------------------------------------------

func TestManagedAndResumeWrapperErrors(t *testing.T) {
	repository, destination, keyPath := firstCapturedRepository(t)

	fakeKey := filepath.Join(t.TempDir(), "nope.key")
	if _, err := ManagedRestore(repository, destination, fakeKey, "personal", false, false); err == nil {
		t.Fatal("ManagedRestore accepted a missing key")
	}
	if _, err := ResumeRestore(repository, fakeKey, "personal", false); err == nil {
		t.Fatal("ResumeRestore accepted a missing key")
	}
	if _, err := DryRunRestore(repository, destination, fakeKey, "personal", false); err == nil {
		t.Fatal("DryRunRestore accepted a missing key")
	}
	if _, err := DryRunRestore(filepath.Join(t.TempDir(), "missing"), destination, keyPath, "personal", false); err == nil {
		t.Fatal("DryRunRestore accepted a missing repository")
	}
	if _, err := ResumeRestore(repository, keyPath, "personal", false); err == nil {
		t.Fatal("ResumeRestore must demand a completed dry-run")
	}
}

func TestManagedRestoreFingerprintAndDryRunBranches(t *testing.T) {
	repository, destination, keyPath := firstCapturedRepository(t)

	// A state saved for destination A must not restore into destination B.
	otherDestination := t.TempDir()
	if _, err := DryRunRestore(repository, destination, keyPath, "personal", false); err != nil {
		t.Fatal(err)
	}
	if _, err := ManagedRestore(repository, otherDestination, keyPath, "personal", false, false); err == nil {
		t.Fatal("restore into a different destination must be refused")
	}
	// A failing path resolver breaks the plan fingerprint comparison.
	overrideAbsPath(t, failingAbsPath())
	if _, err := ManagedRestore(repository, destination, keyPath, "personal", false, false); err == nil {
		t.Fatal("restore accepted a failing path resolver")
	}
	// SaveRestoreState failure surfaces through the dry-run wrapper.
	seamOverride(t, &osCreateTemp, failingCreateTemp())
	if _, err := DryRunRestore(repository, destination, keyPath, "personal", false); err == nil {
		t.Fatal("dry-run persisted its state despite failing temporaries")
	}
}

func TestManagedRestoreMkdirFailure(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "src")
	repository := filepath.Join(root, "repo")
	destination := filepath.Join(root, "dst")
	keyPath := filepath.Join(root, "key")
	key := bytes.Repeat([]byte{31}, KeySize)
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A nested item forces the restore to create destination/nested.
	if err := os.MkdirAll(filepath.Join(source, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "nested", "inner.txt"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	// One pre-existing file at the destination turns its plan into
	// "skip-existing": both dry-run and restore see the same state, so the
	// fingerprints keep matching. Lexical order matters — this item is
	// processed before the nested one that later fails.
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "a.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Capture(source, repository, key); err != nil {
		t.Fatal(err)
	}
	if _, err := DryRunRestore(repository, destination, keyPath, "personal", false); err != nil {
		t.Fatal(err)
	}
	// The destination-mkdir seam fails the nested item's directory creation.
	// A real filesystem occupier cannot be used here: Windows reports a
	// missing path while POSIX reports a non-directory, so the plan itself
	// (correctly) refuses on Linux and the fault would never reach the copy.
	seamOverride(t, &osMkdirAll, func(string, os.FileMode) error {
		return errors.New("destination directory denied")
	})
	report, err := ManagedRestore(repository, destination, keyPath, "personal", false, false)
	if err == nil {
		t.Fatal("restore must fail when the destination directory cannot be created")
	}
	if !verifyHasStatus(report.Results, "failed") {
		t.Fatalf("failed row missing: %#v", report.Results)
	}
	if !verifyHasStatus(report.Results, "skipped-existing") {
		t.Fatalf("skip-existing row missing: %#v", report.Results)
	}
}

func TestManagedRestoreChmodAndStateFaults(t *testing.T) {
	repository, destination, keyPath := firstCapturedRepository(t)
	if _, err := DryRunRestore(repository, destination, keyPath, "personal", false); err != nil {
		t.Fatal(err)
	}
	// The chmod seam fails the permission step after a successful copy.
	seamOverride(t, &osChmod, func(string, os.FileMode) error { return errors.New("chmod refused") })
	if _, err := ManagedRestore(repository, destination, keyPath, "personal", false, false); err == nil {
		t.Fatal("restore must surface the chmod failure")
	}
	// Put the real chmod back before the next sub-case.
	putSeamBack(t, &osChmod, realChmod())
	// A failing state write after a successful copy also aborts.
	second := t.TempDir()
	if _, err := DryRunRestore(repository, second, keyPath, "personal", false); err != nil {
		t.Fatal(err)
	}
	seamOverride(t, &fileWrite, func(*os.File, []byte) (int, error) { return 0, errors.New("disk full") })
	report, err := ManagedRestore(repository, second, keyPath, "personal", false, false)
	if err == nil {
		t.Fatal("restore must surface the state-write failure")
	}
	if !verifyHasStatus(report.Results, "copied") {
		t.Fatalf("first copy should have completed: %#v", report.Results)
	}
}

// putSeamBack installs a production-faithful value again mid-test and keeps a
// cleanup registered so a later t.Fatalf cannot leave a fault injection behind.
func putSeamBack[T any](t *testing.T, slot *T, value T) {
	t.Helper()
	*slot = value
	t.Cleanup(func() { *slot = value })
}

func realChmod() func(string, os.FileMode) error {
	return func(path string, mode os.FileMode) error { return os.Chmod(path, mode) }
}

func TestManagedRestoreInstallFlows(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "portable.key")
	key := bytes.Repeat([]byte{35}, KeySize)
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "a.txt"), []byte("a-plain-text"), 0o644); err != nil {
		t.Fatal(err)
	}
	// No real package manager can execute inside this test.
	t.Setenv("PATH", "")

	// Case 1: destination does not know this manager at all.
	repository := t.TempDir()
	if _, err := Capture(source, repository, key); err != nil {
		t.Fatal(err)
	}
	addApplicationToManifest(t, repository, key, "darwin", []Application{
		{Manager: "brew-brew", Identifier: "definitely-not-a-mapped-package-xq"},
	})
	destination := t.TempDir()
	if _, err := DryRunRestore(repository, destination, keyPath, "personal", false); err != nil {
		t.Fatal(err)
	}
	windows := overridePlatform("windows")
	t.Cleanup(windows)
	report, err := ManagedRestore(repository, destination, keyPath, "personal", false, true)
	if err != nil {
		t.Fatalf("an unsupported plan must not fail the restore: %v", err)
	}
	if !reportHasStatusKind(report.Results, "application", "unsupported") {
		t.Fatalf("unsupported application row missing: %#v", report.Results)
	}
	windows()

	// Case 2: winget import blocked because winget is not on PATH.
	repository2 := t.TempDir()
	if _, err := Capture(source, repository2, key); err != nil {
		t.Fatal(err)
	}
	addApplicationToManifest(t, repository2, key, "windows", []Application{
		{Manager: "winget", Identifier: "Git.Git"},
	})
	destination2 := t.TempDir()
	if _, err := DryRunRestore(repository2, destination2, keyPath, "personal", false); err != nil {
		t.Fatal(err)
	}
	windows = overridePlatform("windows")
	t.Cleanup(windows)
	emptyLookPath := overrideLookPath([]string{})
	t.Cleanup(emptyLookPath)
	report, err = ManagedRestore(repository2, destination2, keyPath, "personal", false, true)
	if err == nil {
		t.Fatal("winget unavailable must fail the application install")
	}
	if !reportHasStatusKind(report.Results, "application", "planned") {
		t.Fatalf("planned application row missing: %#v", report.Results)
	}
	if !reportHasStatusKind(report.Results, "application", "failed") {
		t.Fatalf("failed application row missing: %#v", report.Results)
	}
	emptyLookPath()
	windows()

	// Case 3: the unified per-application branch installs a curated mapping
	// (brew) that fails safely under the empty PATH, next to a refused custom
	// manager that lands in the unsupported rows.
	repository3 := t.TempDir()
	if _, err := Capture(source, repository3, key); err != nil {
		t.Fatal(err)
	}
	addApplicationToManifest(t, repository3, key, "", []Application{
		{Manager: "winget", Identifier: "Git.Git"},
		{Manager: "flying-carpet", Identifier: "x"},
	})
	destination3 := t.TempDir()
	if _, err := DryRunRestore(repository3, destination3, keyPath, "personal", false); err != nil {
		t.Fatal(err)
	}
	darwin := overridePlatform("darwin")
	t.Cleanup(darwin)
	report, err = ManagedRestore(repository3, destination3, keyPath, "personal", false, true)
	if err == nil {
		t.Fatal("uninstallable applications must fail the restore")
	}
	if !reportHasStatusKind(report.Results, "application", "failed") {
		t.Fatalf("failed application rows missing: %#v", report.Results)
	}
	if !reportHasStatusKind(report.Results, "application", "unsupported") {
		t.Fatalf("unsupported application row missing: %#v", report.Results)
	}
}

// ---------------------------------------------------------------------------

func restoreManually(t *testing.T, settings []Setting, destination string, overwrite bool) (Report, error) {
	t.Helper()
	manifest := plainManifestWith(settings)
	report := NewReport("restore")
	err := restoreSettings(manifest, destination, &report, overwrite, false, nil)
	return report, err
}

func TestRestoreSettingsGitBranches(t *testing.T) {
	// Foreign value → unsupported row.
	bad := Setting{Recipe: "git-v1", Values: map[string]string{"evil": "1"}}
	report, err := restoreManually(t, []Setting{bad}, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !reportHasStatusKind(report.Results, "setting", "unsupported") {
		t.Fatalf("git unsupported row missing: %#v", report.Results)
	}
	// Existing file + no overwrite → skipped-existing.
	destination := t.TempDir()
	if err := os.WriteFile(filepath.Join(destination, ".gitconfig"), []byte("[user]"), 0o600); err != nil {
		t.Fatal(err)
	}
	good := Setting{Recipe: "git-v1", Values: map[string]string{"name": "n", "email": "e"}}
	report, err = restoreManually(t, []Setting{good}, destination, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reportHasStatusKind(report.Results, "setting", "skipped-existing") {
		t.Fatalf("git skipped-existing row missing: %#v", report.Results)
	}
	// A directory at the target path fails the write.
	blocked := t.TempDir()
	if err := os.MkdirAll(filepath.Join(blocked, ".gitconfig"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := restoreManually(t, []Setting{good}, blocked, true); err == nil {
		t.Fatal("git write into a directory must fail")
	}
}

func TestRestoreSettingsSSHBranches(t *testing.T) {
	// Foreign recipe → unsupported row.
	bad := Setting{Recipe: "ssh-config-v1", Values: map[string]string{"config": "a", "extra": "b"}}
	report, err := restoreManually(t, []Setting{bad}, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !reportHasStatusKind(report.Results, "setting", "unsupported") {
		t.Fatalf("ssh unsupported row missing: %#v", report.Results)
	}
	// Existing config + no overwrite → skipped.
	destination := t.TempDir()
	if err := os.MkdirAll(filepath.Join(destination, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, ".ssh", "config"), []byte("Host old"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err = restoreManually(t, []Setting{{Recipe: "ssh-config-v1", Values: map[string]string{"config": "Host new"}}}, destination, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reportHasStatusKind(report.Results, "setting", "skipped-existing") {
		t.Fatalf("ssh skipped-existing row missing: %#v", report.Results)
	}
	// .ssh occupied by a file fails mkdir; a directory at config fails write.
	fileBlocked := t.TempDir()
	if err := os.WriteFile(filepath.Join(fileBlocked, ".ssh"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := restoreManually(t, []Setting{{Recipe: "ssh-config-v1", Values: map[string]string{"config": "Host new"}}}, fileBlocked, false); err == nil {
		t.Fatal("ssh mkdir against a file must fail")
	}
	dirBlocked := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dirBlocked, ".ssh", "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := restoreManually(t, []Setting{{Recipe: "ssh-config-v1", Values: map[string]string{"config": "Host new"}}}, dirBlocked, true); err == nil {
		t.Fatal("ssh write into a directory must fail")
	}
}

func TestRestoreSettingsVSCodeBranches(t *testing.T) {
	// renderErr: a recipe without settings.json renders as unsupported.
	render := Setting{Recipe: "vscode-v1", Values: map[string]string{"extensions": "golang.go"}}
	report, err := restoreManually(t, []Setting{render}, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !reportHasStatusKind(report.Results, "setting", "unsupported") {
		t.Fatalf("vscode render row missing: %#v", report.Results)
	}
	// pathErr: an unsupported destination platform has no settings path.
	plan9 := overridePlatform("plan9")
	t.Cleanup(plan9)
	report, err = restoreManually(t, []Setting{plainVSCodeSetting()}, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !reportHasStatusKind(report.Results, "setting", "unsupported") {
		t.Fatalf("vscode path row missing: %#v", report.Results)
	}
	plan9()
	// Remaining branches drive through a controlled config root.
	configRoot := t.TempDir()
	settingsFile := filepath.Join(configRoot, "Code", "User", "settings.json")
	blockedRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(blockedRoot, "Code"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirBlocked := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dirBlocked, "Code", "User", "settings.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	// skipped-existing.
	if err := os.MkdirAll(filepath.Dir(settingsFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsFile, []byte(`{"old":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	setUserConfigRoot(t, configRoot)
	report, err = restoreManually(t, []Setting{plainVSCodeSetting()}, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !reportHasStatusKind(report.Results, "setting", "skipped-existing") {
		t.Fatalf("vscode skipped-existing row missing: %#v", report.Results)
	}
	// mkdir failure: Code is a file.
	setUserConfigRoot(t, blockedRoot)
	if _, err := restoreManually(t, []Setting{plainVSCodeSetting()}, t.TempDir(), false); err == nil {
		t.Fatal("vscode mkdir against a file must fail")
	}
	// write failure: settings.json is a directory; overwrite clears the skip.
	setUserConfigRoot(t, dirBlocked)
	if _, err := restoreManually(t, []Setting{plainVSCodeSetting()}, t.TempDir(), true); err == nil {
		t.Fatal("vscode write into a directory must fail")
	}
	// install flow with a stub runner and the extension value set.
	setUserConfigRoot(t, configRoot)
	restoreLookPath := overrideLookPath([]string{"code"})
	defer restoreLookPath()
	extensionSetting := Setting{Recipe: "vscode-v1", Values: map[string]string{"settings.json": "{}", "extensions": "golang.go"}}
	destination := t.TempDir()
	report = NewReport("restore")
	if err := restoreSettings(plainManifestWith([]Setting{extensionSetting}), destination, &report, true, true, stubRunner(nil, nil)); err != nil {
		t.Fatal(err)
	}
	if !reportHasStatusKind(report.Results, "extension", "installed") {
		t.Fatalf("stubbed extension install row missing: %#v", report.Results)
	}
}

func plainVSCodeSetting() Setting {
	return Setting{Recipe: "vscode-v1", Values: map[string]string{"settings.json": "{}"}}
}

func TestVSCodeSettingsPathUserConfigError(t *testing.T) {
	seamOverride(t, &userConfigDirFunc, func() (string, error) { return "", errors.New("no config dir") })
	if _, err := vscodeSettingsPath("windows"); err == nil {
		t.Fatal("vscodeSettingsPath accepted a failing config resolver")
	}
}

func TestCaptureVSCodeSettingBranches(t *testing.T) {
	// A failing user-config resolver yields an empty recipe, not an error.
	seamOverride(t, &userConfigDirFunc, func() (string, error) { return "", errors.New("no config dir") })
	setting, err := captureVSCodeSetting(context.Background(), stubRunner(nil, nil))
	if err != nil || len(setting.Values) != 0 {
		t.Fatalf("failing resolver: %#v %v", setting.Values, err)
	}
	// Unsupported platform yields an empty recipe (with a valid config root,
	// so the unsupported-platform check itself is what returns early).
	plan9 := overridePlatform("plan9")
	seamOverride(t, &userConfigDirFunc, func() (string, error) { return t.TempDir(), nil })
	setting, err = captureVSCodeSetting(context.Background(), stubRunner(nil, nil))
	if err != nil || len(setting.Values) != 0 {
		t.Fatalf("plan9 capture: %#v %v", setting.Values, err)
	}
	plan9()
	// Back to the production resolver for the host-platform cases.
	seamOverride(t, &userConfigDirFunc, resolveUserConfigDir)
	// An unreadable settings file yields an empty recipe.
	host := overridePlatform(runtime.GOOS)
	defer host()
	root := t.TempDir()
	setUserConfigRoot(t, root)
	if err := os.MkdirAll(filepath.Join(root, "Code", "User", "settings.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	setting, err = captureVSCodeSetting(context.Background(), stubRunner(nil, nil))
	if err != nil || len(setting.Values) != 0 {
		t.Fatalf("blocked settings.json: %#v %v", setting.Values, err)
	}
	// A readable settings file plus a listed "code" binary exercises the
	// extension normalisation: duplicates and malformed identifiers are
	// skipped, real identifiers are kept sorted.
	readableRoot := t.TempDir()
	readableSettings := filepath.Join(readableRoot, "Code", "User", "settings.json")
	if err := os.MkdirAll(filepath.Dir(readableSettings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(readableSettings, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	setUserConfigRoot(t, readableRoot)
	withCode := overrideLookPath([]string{"code"})
	defer withCode()
	setting, err = captureVSCodeSetting(context.Background(), stubRunner([]byte("dup\npub/name\ndup\n"), nil))
	if err != nil || setting.Values["extensions"] != "dup" {
		t.Fatalf("extension normalisation: %#v %v", setting.Values, err)
	}
}

func TestInstallVSCodeExtensionsNilRunner(t *testing.T) {
	// A PATH shim stands in for the real "code" CLI; the nil-runner fallback
	// resolves to the system runner and executes the shim harmlessly.
	shim := t.TempDir()
	name := "code"
	payload := "#!/bin/sh\necho shim-ok\n"
	if runtime.GOOS == "windows" {
		name += ".cmd"
		payload = "@echo off\r\necho shim-ok\r\n"
	}
	if err := os.WriteFile(filepath.Join(shim, name), []byte(payload), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))
	report := NewReport("restore")
	installVSCodeExtensions(&report, []string{"golang.go"}, nil)
	if !reportHasStatusKind(report.Results, "extension", "installed") {
		t.Fatalf("nil-runner install failed: %#v", report.Results)
	}
}

func TestCaptureGitSettingBranches(t *testing.T) {
	// A directory at .gitconfig is a hard error.
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".gitconfig"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := CaptureGitSetting(home); err == nil {
		t.Fatal("a directory at .gitconfig must fail the capture")
	}
	// Comments, blank lines, and malformed assignments are skipped.
	plain := t.TempDir()
	config := "# comment\n\n[user]\n\tname = n\nnoequals line\n[alias]\nst = status\n"
	if err := os.WriteFile(filepath.Join(plain, ".gitconfig"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	setting, err := CaptureGitSetting(plain)
	if err != nil {
		t.Fatalf("a config with comment and malformed lines must parse: %v", err)
	}
	if setting.Values["name"] != "n" || setting.Values["alias.st"] != "status" {
		t.Fatalf("parsed values wrong: %#v", setting.Values)
	}
	// A line beyond the scanner limit surfaces the scanner error.
	huge := t.TempDir()
	payload := "[user]\n\tname = " + strings.Repeat("x", 80_000) + "\n"
	if err := os.WriteFile(filepath.Join(huge, ".gitconfig"), []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CaptureGitSetting(huge); err == nil {
		t.Fatal("an oversized .gitconfig must surface the scanner error")
	}
}

func TestCaptureSSHConfigBranch(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".ssh", "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := CaptureSSHConfig(home); err == nil {
		t.Fatal("a directory at .ssh/config must fail the capture")
	}
}

func TestCaptureChromeBranches(t *testing.T) {
	darwin := overridePlatform("darwin")
	defer darwin()
	home := t.TempDir()
	profile := filepath.Join(home, "Library", "Application Support", "Google", "Chrome", "Default")
	if err := os.MkdirAll(profile, 0o755); err != nil {
		t.Fatal(err)
	}
	// A directory at Bookmarks is a hard error.
	if err := os.MkdirAll(filepath.Join(profile, "Bookmarks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := captureChromeSetting(home); err == nil {
		t.Fatal("a directory at Bookmarks must fail the capture")
	}
	// An Extensions path occupied by a file must degrade or fail, but never
	// fabricate extension identifiers: Windows maps the not-a-directory
	// probe onto not-exist and captures nothing for Chrome.
	bookmarks := filepath.Join(profile, "Bookmarks")
	if err := os.Remove(bookmarks); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bookmarks, []byte(`{"roots":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, "Extensions"), []byte("occupied"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := captureChromeSetting(home); err != nil && runtime.GOOS == "windows" {
		t.Fatalf("windows must degrade gracefully: %v", err)
	}
	// Happy path with real extension directories, sorted.
	extensions := filepath.Join(profile, "Extensions")
	if err := os.Remove(extensions); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(extensions, strings.Repeat("b", 32)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(extensions, strings.Repeat("a", 32)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(extensions, "not-a-chrome-id"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extensions, "loosefile"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ids, err := chromeExtensionIDs(extensions)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != strings.Repeat("a", 32) {
		t.Fatalf("extension identifiers wrong: %#v", ids)
	}
	setting, checklist, err := captureChromeSetting(home)
	if err != nil {
		t.Fatal(err)
	}
	if setting.Values["extensions"] != strings.Join([]string{strings.Repeat("a", 32), strings.Repeat("b", 32)}, "\n") {
		t.Fatalf("extensions value wrong: %#v", setting.Values)
	}
	if len(checklist) != 2 {
		t.Fatalf("checklist wrong: %#v", checklist)
	}
}

func TestRenderChromeBranches(t *testing.T) {
	// Foreign recipe.
	if _, _, err := RenderChromeSetting(Setting{Recipe: "wrong"}); err == nil {
		t.Fatal("foreign chrome recipe accepted")
	}
	// Unsupported value key.
	if _, _, err := RenderChromeSetting(Setting{Recipe: "chrome-v1", Values: map[string]string{"evil": "1"}}); err == nil {
		t.Fatal("evil chrome value accepted")
	}
	// Captured nothing.
	if _, _, err := RenderChromeSetting(Setting{Recipe: "chrome-v1", Values: map[string]string{}}); err == nil {
		t.Fatal("empty chrome recipe accepted")
	}
	// Empty bookmarks database (nothing but whitespace).
	if _, _, err := RenderChromeSetting(Setting{Recipe: "chrome-v1", Values: map[string]string{chromeBookmarksFile: "   "}}); err == nil {
		t.Fatal("empty chrome bookmarks accepted")
	}
	// Extensions-only recipe survives.
	content, extensions, err := RenderChromeSetting(Setting{Recipe: "chrome-v1", Values: map[string]string{"extensions": "a\nb"}})
	if err != nil || len(extensions) != 2 || len(content) != 0 {
		t.Fatalf("extensions-only chrome recipe: %#v %#v %v", content, extensions, err)
	}
}

func TestCaptureOpenCodeBranches(t *testing.T) {
	// A directory at the config path is a hard error.
	home := t.TempDir()
	if err := os.MkdirAll(openCodeConfigPath(home), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := captureOpenCodeSetting(home, false); err == nil {
		t.Fatal("a directory at opencode.json must fail the capture")
	}
	// A directory at auth.json (credentials opt-in) is a hard error.
	authHome := t.TempDir()
	if err := os.MkdirAll(openCodeAuthPath(authHome), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := captureOpenCodeSetting(authHome, true); err == nil {
		t.Fatal("a directory at auth.json must fail the capture")
	}
	// Unparsable config captures nothing.
	jsonHome := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(openCodeConfigPath(jsonHome)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(openCodeConfigPath(jsonHome), []byte("{oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	setting, checklist, err := captureOpenCodeSetting(jsonHome, false)
	if err != nil || len(setting.Values) != 0 || len(checklist) != 0 {
		t.Fatalf("unparsable config: %#v %v", setting.Values, err)
	}
	// Credential-shaped values are scrubbed and recorded as excluded.
	secretHome := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(openCodeConfigPath(secretHome)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(openCodeConfigPath(secretHome), []byte(`{"model":"sk-XXXXXXXXXXXXXXXXXXXXXXXXX"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	setting, _, err = captureOpenCodeSetting(secretHome, false)
	if err != nil || len(setting.Excluded) == 0 {
		t.Fatalf("credential-shaped config not excluded: %#v %v", setting.Excluded, err)
	}
	// auth.json travels only with the opt-in and adds its checklist row.
	authOK := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(openCodeConfigPath(authOK)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(openCodeConfigPath(authOK), []byte(`{"model":"opus"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(openCodeAuthPath(authOK)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(openCodeAuthPath(authOK), []byte(`{"anthropic":"key"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	setting, checklist, err = captureOpenCodeSetting(authOK, true)
	if err != nil || setting.Values[openCodeAuthFile] == "" || len(checklist) == 0 {
		t.Fatalf("credentials opt-in: %#v %#v %v", setting.Values, checklist, err)
	}
	// Without the opt-in the checklist asks for a fresh sign-in.
	setting, checklist, err = captureOpenCodeSetting(authOK, false)
	if err != nil || setting.Values[openCodeAuthFile] != "" || len(checklist) == 0 {
		t.Fatalf("default credentials behaviour: %#v %#v %v", setting.Values, checklist, err)
	}
}

func TestRenderOpenCodeBranches(t *testing.T) {
	// Foreign recipe.
	if _, _, err := RenderOpenCodeSetting(Setting{Recipe: "wrong"}); err == nil {
		t.Fatal("foreign opencode recipe accepted")
	}
	// Unsupported value key.
	if _, _, err := RenderOpenCodeSetting(Setting{Recipe: "opencode-v1", Values: map[string]string{"evil": "1"}}); err == nil {
		t.Fatal("evil opencode value accepted")
	}
	// A credential shape inside the config is refused at restore time.
	blocked := Setting{Recipe: "opencode-v1", Values: map[string]string{openCodeConfigFile: `{"model":"sk-XXXXXXXXXXXXXXXXXXXXXXXXXXXX3"}`}}
	if _, _, err := RenderOpenCodeSetting(blocked); err == nil {
		t.Fatal("credential-shaped opencode config accepted")
	}
	// Clean config and auth travel together.
	content, auth, err := RenderOpenCodeSetting(Setting{Recipe: "opencode-v1", Values: map[string]string{
		openCodeConfigFile: `{"model":"opus"}`,
		openCodeAuthFile:   `{"anthropic":"key"}`,
	}})
	if err != nil || string(content) != `{"model":"opus"}` || auth != `{"anthropic":"key"}` {
		t.Fatalf("clean opencode recipe: %s %q %v", content, auth, err)
	}
}

func TestCredentialShapeJSON(t *testing.T) {
	// Unparsable JSON is treated as a credential carrier.
	if !credentialsShapeInJSON([]byte("{oops")) {
		t.Fatal("unparsable JSON must be treated as credential-shaped")
	}
	// A nested secret key is found.
	if !credentialsShapeInJSON([]byte(`{"outer":{"token":"x"}}`)) {
		t.Fatal("nested secret key not detected")
	}
	// A secret inside an array is found.
	if !credentialsShapeInJSON([]byte(`{"keys":["sk-XXXXXXXXXXXXXXXXXXXXXXXXX"]}`)) {
		t.Fatal("array secret not detected")
	}
	// Benign arrays are walked element-by-element without a false hit; this
	// is the only shape that reaches the array walk instead of the flattened
	// text check.
	if credentialsShapeInJSON([]byte(`{"groups":{"path":["a","b"]},"joined":["one","two"]}`)) {
		t.Fatal("benign arrays flagged as credential-shaped")
	}
	// Secret keys nested inside arrays of objects are the one shape only the
	// structured array walk can catch: the flattened-text check cannot see
	// map keys.
	if !credentialsShapeInJSON([]byte(`{"marks":[{"token":"x"}]}`)) {
		t.Fatal("secret key under an array of objects not detected")
	}
	// A clean document passes.
	if credentialsShapeInJSON([]byte(`{"model":"opus","profile":"work"}`)) {
		t.Fatal("clean document flagged as credential-shaped")
	}
}

func TestScrubVarNames(t *testing.T) {
	for name, want := range map[string]bool{
		"token":         true,
		"secret":        true,
		"password":      true,
		"passwd":        true,
		"credential":    true,
		"credentials":   true,
		"auth":          true,
		"auth.json":     true,
		"authorization": true,
		"bearer":        true,
		"jwt":           true,
		"cookie":        true,
		"apikey":        true,
		"GITHUB_TOKEN":  true,
		"api_key":       true,
		"access_key":    true,
		"secret_key":    true,
		// Exact words added with the widened guard.
		"pat":         true,
		"pfx":         true,
		"privatekey":  true,
		"private_key": true,
		"key_id":      false,
		"my_key":      false,
		"":            false,
		"path":        false,
		"private":     false,
	} {
		if got := isSecretVarName(name); got != want {
			t.Fatalf("isSecretVarName(%q) = %v, want %v", name, got, want)
		}
	}
	// Line-level shape and name guards.
	for line, want := range map[string]bool{
		"export GITHUB_TOKEN=ghp_x":   true,
		"password":                    true,
		"password=hunter2":            true,
		"https://user:secret@host/x":  true,
		"export PATH=/usr/bin:/bin":   false,
		"# password":                  false,
		"":                            false,
		"sk-XXXXXXXXXXXXXXXXXXXXXXXX": true,
		"  secret: hidden-token":      true,
	} {
		if got := lineDropsCredential(line); got != want {
			t.Fatalf("lineDropsCredential(%q) = %v, want %v", line, got, want)
		}
	}
}

func TestScrubKubeConfigLines(t *testing.T) {
	config := []byte("apiVersion: v1\ntoken: xyz\npassword: hunter2\nsecret: hidden-token\nclusters:\n- name: demo\n")
	scrubbed, excluded, dropped := scrubKubeConfig(config)
	if !dropped || len(excluded) < 3 {
		t.Fatalf("kubeconfig scrub missed credentials: %#v", excluded)
	}
	if strings.Contains(string(scrubbed), "hunter2") || strings.Contains(string(scrubbed), "xyz") || strings.Contains(string(scrubbed), "hidden-token") {
		t.Fatalf("credentials survived the kube scrub: %s", scrubbed)
	}
	if !strings.Contains(string(scrubbed), "clusters:") {
		t.Fatalf("benign kube content lost: %s", scrubbed)
	}
}

func TestScrubVSCodeArrays(t *testing.T) {
	document := []byte(`{"env":["sk-XXXXXXXXXXXXXXXXXXXX","ok"],"terraform.token":"x","path":["a","b"]}`)
	scrubbed, excluded := scrubVSCodeSecrets(document)
	// The conservative policy drops the whole "env" key: one credential
	// shape inside the array poisons every entry it contains.
	if len(excluded) < 2 {
		t.Fatalf("array/key secrets not excluded: %#v", excluded)
	}
	var cleaned map[string]interface{}
	if err := json.Unmarshal(scrubbed, &cleaned); err != nil {
		t.Fatal(err)
	}
	if _, present := cleaned["env"]; present {
		t.Fatal("a credential-shaped array survived the scrub")
	}
	if _, present := cleaned["terraform.token"]; present {
		t.Fatal("secret key survived the scrub")
	}
	if _, present := cleaned["path"]; !present {
		t.Fatal("benign array was dropped")
	}
}

// ---------------------------------------------------------------------------

func TestInventoryRunnerFallbacks(t *testing.T) {
	ctx := context.Background()
	// inventoryApplications falls back to the system runner before refusing
	// an unsupported platform.
	unsupported := overridePlatform("plan9")
	t.Cleanup(unsupported)
	if _, err := inventoryApplications(ctx, nil, t.TempDir()); err == nil {
		t.Fatal("unsupported platform must fail even with a nil runner")
	}
	unsupported()
	// inventoryWindowsApplications: nil runner then the winget guard.
	windows := overridePlatform("windows")
	t.Cleanup(windows)
	emptyLookPath := overrideLookPath([]string{})
	t.Cleanup(emptyLookPath)
	if _, err := inventoryWindowsApplications(ctx, nil, t.TempDir()); err == nil {
		t.Fatal("inventory must refuse without winget")
	}
	if _, err := inventoryApplications(ctx, nil, t.TempDir()); err == nil {
		t.Fatal("inventory must refuse without winget")
	}
	emptyLookPath()
	windows()
	// inventoryLinuxApplications: nil runner then the manager guard.
	linux := overridePlatform("linux")
	t.Cleanup(linux)
	// Keep the probe hermetic: on a real Linux host any distro manager
	// (apk on Alpine, apt on Debian) would otherwise be found and run.
	emptyLinuxLookPath := overrideLookPath([]string{})
	t.Cleanup(emptyLinuxLookPath)
	if _, err := inventoryLinuxApplications(ctx, nil); err == nil {
		t.Fatal("linux inventory must refuse with no managers")
	}
}

func TestInventoryDarwinRunnerFailures(t *testing.T) {
	darwin := overridePlatform("darwin")
	defer darwin()
	restoreLookPath := overrideLookPath([]string{"brew"})
	defer restoreLookPath()
	// brew failing.
	if _, err := inventoryDarwinApplications(context.Background(), stubRunner(nil, errors.New("brew failed")), t.TempDir()); err == nil {
		t.Fatal("brew failure must surface")
	}
	// brew succeeding but writing no Brewfile.
	if _, err := inventoryDarwinApplications(context.Background(), stubRunner(nil, nil), t.TempDir()); err == nil {
		t.Fatal("a missing Brewfile must surface")
	}
}

func TestInstallWindowsAppsWingetMissing(t *testing.T) {
	windows := overridePlatform("windows")
	defer windows()
	restoreLookPath := overrideLookPath([]string{})
	defer restoreLookPath()
	err := installWindowsApplications(context.Background(), stubRunner(nil, nil), []Application{{Manager: "winget", Identifier: "x"}}, filepath.Join(t.TempDir(), "i.json"))
	if err == nil || !strings.Contains(err.Error(), "winget is unavailable") {
		t.Fatalf("winget-missing guard wrong: %v", err)
	}
}

func TestSystemCommandRunnerSuccess(t *testing.T) {
	runner := systemCommandRunner{}
	var output []byte
	var err error
	if runtime.GOOS == "windows" {
		output, err = runner.Run(context.Background(), "cmd", "/c", "echo", "hello")
	} else {
		output, err = runner.Run(context.Background(), "sh", "-c", "echo hello")
	}
	if err != nil || !strings.Contains(string(output), "hello") {
		t.Fatalf("system runner success broken: %q %v", output, err)
	}
}

// ---------------------------------------------------------------------------

func TestPackDriveWriteFailures(t *testing.T) {
	repository := firstCapturedRepositoryDir(t)
	keyPath := filepath.Join(t.TempDir(), "portable.key")
	key := bytes.Repeat([]byte{52}, KeySize)
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}
	// The drive key cannot be written when its target is a directory.
	if err := os.MkdirAll(filepath.Join(repository, portableKeyFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := PackPortableDrive(repository, keyPath, ""); err == nil {
		t.Fatal("pack accepted a blocked drive key")
	}
	if err := os.Remove(filepath.Join(repository, portableKeyFile)); err != nil {
		t.Fatal(err)
	}
	// A binary target already occupied by a non-empty directory fails the copy.
	occupied := filepath.Join(repository, portableBinaryNames[0])
	if err := os.MkdirAll(filepath.Join(occupied, "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	binaries := t.TempDir()
	if err := os.WriteFile(filepath.Join(binaries, portableBinaryNames[0]), []byte("bb"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := PackPortableDrive(repository, keyPath, binaries); err == nil {
		t.Fatal("pack accepted an occupied binary target")
	}
	if err := os.RemoveAll(occupied); err != nil {
		t.Fatal(err)
	}
	// The chmod seam failing fails the pack.
	seamOverride(t, &osChmod, func(string, os.FileMode) error { return errors.New("chmod refused") })
	if _, err := PackPortableDrive(repository, keyPath, binaries); err == nil {
		t.Fatal("pack surfaced the chmod failure")
	}
	// Launcher and readme writes fail against occupied paths.
	for index, name := range []string{portableLauncherPOSX, portableLauncherMac, portableLauncherWin, portableReadmeFile} {
		repo := firstCapturedRepositoryDir(t)
		if err := os.MkdirAll(filepath.Join(repo, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := PackPortableDrive(repo, keyPath, ""); err == nil {
			t.Fatalf("pack accepted an occupied %s (case %d)", name, index)
		}
	}
}

func TestCopyPortableFileFaults(t *testing.T) {
	binaries := t.TempDir()
	source := filepath.Join(binaries, "src.bin")
	if err := os.WriteFile(source, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A directory as source fails the streamed write.
	if err := copyPortableFile(binaries, filepath.Join(t.TempDir(), "out"), 0); err == nil {
		t.Fatal("copy accepted a directory source")
	}
	// Sync and close failures surface after a successful write.
	target := filepath.Join(t.TempDir(), "out")
	seamOverride(t, &fileSync, func(*os.File) error { return errors.New("sync refused") })
	if err := copyPortableFile(source, target, 7); err == nil {
		t.Fatal("copy surfaced the sync failure")
	}
	seamOverride(t, &fileSync, func(f *os.File) error { return f.Sync() })
	seamOverride(t, &fileClose, failingClose())
	if err := copyPortableFile(source, target, 7); err == nil {
		t.Fatal("copy surfaced the close failure")
	}
}

// ---------------------------------------------------------------------------

func TestFaultSeamObjectLayer(t *testing.T) {
	key := bytes.Repeat([]byte{57}, KeySize)
	root := t.TempDir()
	source := filepath.Join(root, "data.txt")
	if err := os.WriteFile(source, []byte("object-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	item := Item{ID: "obj", SHA256: mustFileHash(t, source), Size: 12}
	objectPath := filepath.Join(root, "obj.bin")

	// Fresh encryption; then the reuse branch and the replace branch.
	if err := encryptFile(source, objectPath, key, item.ID); err != nil {
		t.Fatal(err)
	}
	if err := reuseOrEncryptFile(source, objectPath, key, item); err != nil {
		t.Fatalf("reuse branch: %v", err)
	}
	if err := os.WriteFile(objectPath, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := reuseOrEncryptFile(source, objectPath, key, item); err != nil {
		t.Fatalf("replace branch: %v", err)
	}
	if err := reuseOrEncryptFile(source, objectPath, key, item); err != nil {
		t.Fatalf("reuse after replace: %v", err)
	}
	// The injected encrypt failure surfaces through both wrappers.
	seamOverride(t, &encryptObjectFunc, func(string, string, []byte, string) error { return errors.New("encrypt refused") })
	if err := reuseOrEncryptFile(source, filepath.Join(t.TempDir(), "fresh.bin"), key, item); err == nil {
		t.Fatal("reuseOrEncryptFile surfaced the encrypt failure")
	}
	if err := encryptTempThenRename(source, filepath.Join(t.TempDir(), "out.bin"), key, item.ID); err == nil {
		t.Fatal("encryptTempThenRename surfaced the encrypt failure")
	}
	// A directory source fails the streamed read (fresh object path so the
	// reuse branch cannot short-circuit the read).
	putSeamBack(t, &encryptObjectFunc, encryptFile)
	if err := reuseOrEncryptFile(root, filepath.Join(t.TempDir(), "newobject.bin"), key, item); err == nil {
		t.Fatal("a directory source must fail the object encryption")
	}

	// encryptTempThenRename failure ladder.
	seamOverride(t, &encryptObjectFunc, encryptFile)
	faultOut := filepath.Join(t.TempDir(), "out.bin")
	seamOverride(t, &osCreateTemp, failingCreateTemp())
	if err := encryptTempThenRename(source, faultOut, key, item.ID); err == nil {
		t.Fatal("encryptTempThenRename surfaced the temporary failure")
	}
	seamOverride(t, &osCreateTemp, os.CreateTemp)
	seamOverride(t, &fileClose, failingClose())
	if err := encryptTempThenRename(source, faultOut, key, item.ID); err == nil {
		t.Fatal("encryptTempThenRename surfaced the close failure")
	}
	seamOverride(t, &fileClose, fileCloseReal())
	seamOverride(t, &removePath, func(string) error { return errors.New("remove refused") })
	if err := encryptTempThenRename(source, faultOut, key, item.ID); err == nil {
		t.Fatal("encryptTempThenRename surfaced the remove failure")
	}
	seamOverride(t, &removePath, os.Remove)
	if err := encryptTempThenRename(source, faultOut, key, item.ID); err != nil {
		t.Fatalf("clean encryptTempThenRename: %v", err)
	}
}

func TestEncryptFileFaultLadder(t *testing.T) {
	key := bytes.Repeat([]byte{58}, KeySize)
	root := t.TempDir()
	source := filepath.Join(root, "data.txt")
	if err := os.WriteFile(source, []byte("bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Missing source.
	if err := encryptFile(filepath.Join(root, "missing"), filepath.Join(root, "o.bin"), key, "id"); err == nil {
		t.Fatal("encryptFile accepted a missing source")
	}
	// Occupied target (O_EXCL).
	target := filepath.Join(root, "occupied.bin")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := encryptFile(source, target, key, "id"); err == nil {
		t.Fatal("encryptFile accepted an occupied target")
	}
	// The magic-header write fails through the seam.
	seamOverride(t, &fileWrite, func(*os.File, []byte) (int, error) { return 0, errors.New("disk full") })
	if err := encryptFile(source, filepath.Join(root, "m.bin"), key, "id"); err == nil {
		t.Fatal("encryptFile surfaced the magic-write failure")
	}
	// The chunk write fails through the seam.
	seamOverride(t, &fileWrite, fileWriteReal())
	seamOverride(t, &writeObjectChunk, func(io.Writer, []byte) error { return errors.New("chunk refused") })
	if err := encryptFile(source, filepath.Join(root, "c.bin"), key, "id"); err == nil {
		t.Fatal("encryptFile surfaced the chunk failure")
	}
	seamOverride(t, &writeObjectChunk, writeChunk)
	// Multi-chunk round trip: a full chunk plus one chunk and a short tail.
	plainSize := chunkSize + 1024
	if err := os.WriteFile(source, bytes.Repeat([]byte("z"), plainSize), 0o644); err != nil {
		t.Fatal(err)
	}
	multi := filepath.Join(root, "multi.bin")
	if err := encryptFile(source, multi, key, "multi"); err != nil {
		t.Fatal(err)
	}
	hash, size, err := verifyFile(multi, key, "multi")
	if err != nil {
		t.Fatalf("multi-chunk verify: %v", err)
	}
	if size != int64(plainSize) {
		t.Fatalf("multi-chunk size wrong: %d", size)
	}
	decoded := filepath.Join(root, "decoded.txt")
	if err := decryptFile(multi, decoded, key, "multi", hash, size); err != nil {
		t.Fatalf("multi-chunk decrypt: %v", err)
	}
	if got, readErr := os.ReadFile(decoded); readErr != nil || len(got) != plainSize {
		t.Fatalf("multi-chunk content wrong: %d %v", len(got), readErr)
	}
}

func TestChunkWireFormatBranches(t *testing.T) {
	// writeChunk refuses oversized payloads.
	if err := writeChunk(&bytes.Buffer{}, make([]byte, maxWireChunk+1)); err == nil {
		t.Fatal("writeChunk accepted an oversized payload")
	}
	// writeChunk surfaces the length-header and payload write failures.
	if err := writeChunk(&stepWriter{failAt: 1}, []byte("payload-bytes")); err == nil {
		t.Fatal("writeChunk surfaced the header-write failure")
	}
	if err := writeChunk(&stepWriter{failAt: 2}, []byte("payload-bytes")); err == nil {
		t.Fatal("writeChunk surfaced the payload-write failure")
	}
	// readChunk branches.
	if _, err := readChunk(&bytes.Buffer{}); !errors.Is(err, io.EOF) {
		t.Fatalf("empty stream: %v", err)
	}
	short := &bytes.Buffer{}
	short.WriteString("ab")
	if _, err := readChunk(short); err == nil {
		t.Fatal("short header accepted")
	}
	small := &bytes.Buffer{}
	binary4(small, 3)
	if _, err := readChunk(small); err == nil {
		t.Fatal("undersized chunk accepted")
	}
	large := &bytes.Buffer{}
	binary4(large, maxWireChunk+1)
	if _, err := readChunk(large); err == nil {
		t.Fatal("oversized chunk accepted")
	}
	truncated := &bytes.Buffer{}
	binary4(truncated, 28) // header accepted, payload missing
	if _, err := readChunk(truncated); err == nil {
		t.Fatal("truncated payload accepted")
	}
	// A successful readChunk round trip feeds decodeObjectStream.
	key := bytes.Repeat([]byte{59}, KeySize)
	payload := []byte("decode-me")
	object := &bytes.Buffer{}
	object.WriteString("MKF1")
	chunk, err := encryptBytes(key, payload, chunkAAD("oid", 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeChunk(object, chunk); err != nil {
		t.Fatal(err)
	}
	output := &bytes.Buffer{}
	hash, size, err := decodeObjectStream(object, output, key, "oid")
	if err != nil || string(output.Bytes()) != "decode-me" || hash != mustFileHash(t, writeTempBytes(t, payload)) || size != int64(len(payload)) {
		t.Fatalf("decode round trip: %q %d %v", output.Bytes(), size, err)
	}
	// Short magic.
	if _, _, err := decodeObjectStream(bytes.NewReader(nil), output, key, "oid"); err == nil {
		t.Fatal("short magic accepted")
	}
	// Wrong magic.
	bad := &bytes.Buffer{}
	bad.WriteString("NOPE")
	if _, _, err := decodeObjectStream(bad, output, key, "oid"); err == nil {
		t.Fatal("wrong magic accepted")
	}
	// A corrupted chunk fails authentication.
	authFailed := &bytes.Buffer{}
	authFailed.WriteString("MKF1")
	corrupted := append([]byte(nil), chunk...)
	corrupted[20] ^= 0xFF
	if err := writeChunk(authFailed, corrupted); err != nil {
		t.Fatal(err)
	}
	if _, _, err := decodeObjectStream(authFailed, output, key, "oid"); err == nil {
		t.Fatal("corrupted chunk accepted")
	}
	// A failing writer surfaces mid-stream.
	decoder := &bytes.Buffer{}
	decoder.WriteString("MKF1")
	if err := writeChunk(decoder, chunk); err != nil {
		t.Fatal(err)
	}
	if _, _, err := decodeObjectStream(decoder, refuseWriter{}, key, "oid"); err == nil {
		t.Fatal("failing writer accepted")
	}
	// decryptBytes: short and tampered ciphertexts.
	if _, err := decryptBytes(key, []byte("short"), []byte("ctx")); err == nil {
		t.Fatal("short ciphertext accepted")
	}
	tampered := append([]byte(nil), chunk...)
	tampered[len(tampered)-1] ^= 0xFF
	if _, err := decryptBytes(key, tampered, chunkAAD("oid", 0)); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
	// newGCM and the seal/open helpers reject malformed keys.
	if _, err := encryptBytes(bytes.Repeat([]byte{1}, KeySize-1), []byte("x"), nil); err == nil {
		t.Fatal("encryptBytes accepted a short key")
	}
	if _, err := decryptBytes(bytes.Repeat([]byte{1}, KeySize-1), []byte("x"), nil); err == nil {
		t.Fatal("decryptBytes accepted a short key")
	}
}

func failingClose() func(*os.File) error {
	// A failed close still releases the handle on every platform, which is
	// what the fault injection must model to keep TempDir cleanup safe.
	return func(f *os.File) error {
		_ = f.Close()
		return errors.New("close refused")
	}
}

type stepWriter struct {
	written int
	failAt  int
}

func (writer *stepWriter) Write(p []byte) (int, error) {
	writer.written++
	if writer.written == writer.failAt {
		return 0, errors.New("stream refused")
	}
	return len(p), nil
}

type refuseWriter struct{}

func (refuseWriter) Write(p []byte) (int, error) {
	return 0, errors.New("stream refused")
}

func binary4(buffer *bytes.Buffer, value uint32) {
	buffer.WriteByte(byte(value >> 24))
	buffer.WriteByte(byte(value >> 16))
	buffer.WriteByte(byte(value >> 8))
	buffer.WriteByte(byte(value))
}

func TestDecryptFileFaultLadder(t *testing.T) {
	key := bytes.Repeat([]byte{60}, KeySize)
	root := t.TempDir()
	source := filepath.Join(root, "data.txt")
	if err := os.WriteFile(source, []byte("payload-again"), 0o644); err != nil {
		t.Fatal(err)
	}
	plainHash := mustFileHash(t, source)
	objectPath := filepath.Join(root, "obj.bin")
	if err := encryptFile(source, objectPath, key, "oid"); err != nil {
		t.Fatal(err)
	}
	// Success.
	target := filepath.Join(root, "out.bin")
	if err := decryptFile(objectPath, target, key, "oid", plainHash, 13); err != nil {
		t.Fatal(err)
	}
	// Missing object.
	if err := decryptFile(filepath.Join(root, "missing"), target, key, "oid", plainHash, 13); err == nil {
		t.Fatal("decryptFile accepted a missing object")
	}
	// Failure ladder through the seams.
	seamOverride(t, &osCreateTemp, failingCreateTemp())
	if err := decryptFile(objectPath, target, key, "oid", plainHash, 13); err == nil {
		t.Fatal("decryptFile surfaced the temporary failure")
	}
	seamOverride(t, &osCreateTemp, os.CreateTemp)
	seamOverride(t, &fileSync, func(*os.File) error { return errors.New("sync refused") })
	if err := decryptFile(objectPath, target, key, "oid", plainHash, 13); err == nil {
		t.Fatal("decryptFile surfaced the sync failure")
	}
	seamOverride(t, &fileSync, func(f *os.File) error { return f.Sync() })
	seamOverride(t, &fileClose, failingClose())
	if err := decryptFile(objectPath, target, key, "oid", plainHash, 13); err == nil {
		t.Fatal("decryptFile surfaced the close failure")
	}
	seamOverride(t, &fileClose, fileCloseReal())
	// Digest and size mismatches.
	if err := decryptFile(objectPath, target, key, "oid", strings.Repeat("ab", 32), 13); err == nil {
		t.Fatal("decryptFile accepted a wrong digest")
	}
	if err := decryptFile(objectPath, target, key, "oid", plainHash, 999); err == nil {
		t.Fatal("decryptFile accepted a wrong size")
	}
	// Rename fail: the target is an occupied directory.
	occupied := filepath.Join(root, "occupied")
	if err := os.MkdirAll(filepath.Join(occupied, "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := decryptFile(objectPath, occupied, key, "oid", plainHash, 13); err == nil {
		t.Fatal("decryptFile renamed over an occupied directory")
	}
	// An object with the wrong magic header.
	badObject := filepath.Join(root, "bad.bin")
	if err := os.WriteFile(badObject, []byte("XXXX"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := decryptFile(badObject, target, key, "oid", plainHash, 13); err == nil {
		t.Fatal("decryptFile accepted a wrong magic header")
	}
}

// ---------------------------------------------------------------------------

func TestCaptureWithOptionsFaultLadder(t *testing.T) {
	key := bytes.Repeat([]byte{61}, KeySize)
	root := t.TempDir()
	source := filepath.Join(root, "src")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Repository inside the source root is refused.
	if _, err := CaptureWithOptions(source, filepath.Join(source, "repo"), key, false, false, nil); err == nil {
		t.Fatal("capture accepted a repository inside the source root")
	}
	// The objects directory cannot be created when its parent is a file.
	occupied := filepath.Join(root, "occupied")
	if err := os.WriteFile(occupied, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CaptureWithOptions(source, filepath.Join(occupied, "repo"), key, false, false, nil); err == nil {
		t.Fatal("capture accepted an uncreatable repository")
	}
	// The format marker write fails when its path is a directory.
	withFormatDir := filepath.Join(root, "repo-with-blocked-format")
	if err := os.MkdirAll(withFormatDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(withFormatDir, formatFile), []byte(formatVersion), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(withFormatDir, formatFile)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(withFormatDir, formatFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := CaptureWithOptions(source, withFormatDir, key, false, false, nil); err == nil {
		t.Fatal("capture accepted a blocked format marker")
	}
	// A missing source root fails loudly instead of capturing nothing.
	if _, err := CaptureWithOptions(filepath.Join(root, "missing"), t.TempDir(), key, false, false, nil); err == nil {
		t.Fatal("capture accepted a missing source root")
	}
	// includeApplications with a failing workspace creation aborts.
	seamOverride(t, &osMkdirTemp, func(string, string) (string, error) { return "", errors.New("no workspaces") })
	if _, err := CaptureWithOptions(source, t.TempDir(), key, true, false, nil); err == nil {
		t.Fatal("capture surfaced the workspace failure")
	}
	seamOverride(t, &osMkdirTemp, os.MkdirTemp)
	// includeApplications with the platform tooling unavailable: the capture
	// records the exclusion instead of failing.
	windows := overridePlatform("windows")
	t.Cleanup(windows)
	emptyLookPath := overrideLookPath([]string{})
	t.Cleanup(emptyLookPath)
	manifest, err := CaptureWithOptions(source, t.TempDir(), key, true, false, nil)
	if err != nil || len(manifest.Applications) != 0 {
		t.Fatalf("unavailable tooling must yield a clean capture: %#v %v", manifest.Applications, err)
	}
	if len(manifest.Exclusions) == 0 {
		t.Fatal("the tooling exclusion is not recorded")
	}
	emptyLookPath()
	windows()
	// The object-encrypt failure aborts capture.
	seamOverride(t, &encryptObjectFunc, func(string, string, []byte, string) error { return errors.New("encrypt refused") })
	if _, err := CaptureWithOptions(source, t.TempDir(), key, false, false, nil); err == nil {
		t.Fatal("capture surfaced the object-encrypt failure")
	}
	// The final manifest write fails when its path is a directory.
	manifestBlocked := t.TempDir()
	if err := os.MkdirAll(filepath.Join(manifestBlocked, manifestFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := CaptureWithOptions(source, manifestBlocked, key, false, false, nil); err == nil {
		t.Fatal("capture accepted a blocked manifest path")
	}
}

func TestCapturePartialInventoryDarwin(t *testing.T) {
	key := bytes.Repeat([]byte{62}, KeySize)
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	darwin := overridePlatform("darwin")
	defer darwin()
	restoreLookPath := overrideLookPath([]string{"brew"})
	defer restoreLookPath()
	// A Brewfile mixing supported intents and unsupported lines keeps the
	// intents and records the exclusion.
	messy := "brew git\nbrew \"\"\nvscode golang.go\n"
	manifest, err := CaptureWithOptions(source, t.TempDir(), key, true, false, fakeRunnerWriter(messy))
	if err != nil {
		t.Fatalf("partial darwin inventory: %v", err)
	}
	if len(manifest.Applications) != 1 || manifest.Applications[0].Identifier != "git" {
		t.Fatalf("partial inventory wrong: %#v", manifest.Applications)
	}
	found := false
	for _, exclusion := range manifest.Exclusions {
		if strings.Contains(exclusion.Reason, "inventory incomplete") {
			found = true
		}
	}
	if !found {
		t.Fatalf("Brewfile exclusion missing: %#v", manifest.Exclusions)
	}
}

func TestCaptureRecordsSettingAndDevRecipes(t *testing.T) {
	key := bytes.Repeat([]byte{63}, KeySize)
	source := t.TempDir()
	// .ssh/config occupied by a directory records a settings exclusion.
	if err := os.MkdirAll(filepath.Join(source, ".ssh", "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Dev-environment recipe content.
	writeDevFixture(t, source, ".bashrc", "export PATH=/usr/local/bin:$PATH\n")
	writeDevFixture(t, source, ".docker/config.json", `{"credsStore":"desktop"}`)
	// Chrome content with a checklist side effect (darwin layout).
	darwin := overridePlatform("darwin")
	profile := filepath.Join(source, "Library", "Application Support", "Google", "Chrome", "Default")
	if err := os.MkdirAll(profile, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, "Bookmarks"), []byte(`{"roots":{"bookmark_bar":{}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	repository := t.TempDir()
	manifest, err := CaptureWithOptions(source, repository, key, false, false, stubRunner(nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Settings) < 3 {
		t.Fatalf("settings not captured: %#v", manifest.Settings)
	}
	if len(manifest.Checklist) == 0 {
		t.Fatal("chrome checklist missing")
	}
	sawExclusion := false
	for _, exclusion := range manifest.Exclusions {
		if strings.Contains(exclusion.Reason, "setting capture unavailable") {
			sawExclusion = true
		}
	}
	if !sawExclusion {
		t.Fatalf("ssh exclusion missing: %#v", manifest.Exclusions)
	}
	// The captured settings restore cleanly (catalog round trip included).
	darwin()
	destination := t.TempDir()
	report := NewReport("restore")
	if err := restoreSettings(manifest, destination, &report, true, false, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(destination, ".docker", "config.json")); err != nil {
		t.Fatal(err)
	}
	if len(report.Results) < 3 {
		t.Fatalf("setting rows missing: %#v", report.Results)
	}
}

// ---------------------------------------------------------------------------

func TestLoadManifestAndValidateBranches(t *testing.T) {
	key := bytes.Repeat([]byte{64}, KeySize)
	repository := t.TempDir()
	// Missing repository.
	if _, err := LoadManifest(repository, key); err == nil {
		t.Fatal("LoadManifest accepted a missing repository")
	}
	// Wrong key.
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Capture(source, repository, key); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifest(repository, bytes.Repeat([]byte{65}, KeySize)); err == nil {
		t.Fatal("LoadManifest accepted a wrong key")
	}
	// Unsupported version.
	forged := func(manifest Manifest) []byte {
		t.Helper()
		encoded, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		ciphertext, err := encryptBytes(key, encoded, []byte("manifest-v1"))
		if err != nil {
			t.Fatal(err)
		}
		return ciphertext
	}
	if err := os.WriteFile(filepath.Join(repository, manifestFile), forged(Manifest{Version: 2, SourceOS: "linux"}), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifest(repository, key); err == nil {
		t.Fatal("LoadManifest accepted version 2")
	}
	// validateManifest item guards. Hex checks need full-length values so the
	// per-character loop is what rejects them, not the length check.
	badID := strings.Repeat("ab", 31) + "zz"
	badSHA := strings.Repeat("cd", 31) + "zz"
	goodID := strings.Repeat("ab", 32)
	goodSHA := strings.Repeat("cd", 32)
	twoID := []Item{
		{ID: goodID, RelativePath: "a.txt", SHA256: goodSHA},
		{ID: goodID, RelativePath: "a.txt", SHA256: goodSHA},
	}
	twoPaths := []Item{
		{ID: goodID, RelativePath: "a.txt", SHA256: goodSHA},
		{ID: strings.Repeat("ac", 32), RelativePath: "a.txt", SHA256: goodSHA},
	}
	if err := validateManifest(Manifest{Version: 1, Items: []Item{{ID: goodID, RelativePath: "../escape", SHA256: goodSHA}}}); err == nil {
		t.Fatal("validateManifest accepted a traversal path")
	}
	if err := validateManifest(Manifest{Version: 1, Items: []Item{{ID: badID, RelativePath: "a.txt", SHA256: goodSHA}}}); err == nil {
		t.Fatal("validateManifest accepted a non-hex identifier")
	}
	if err := validateManifest(Manifest{Version: 1, Items: twoID}); err == nil {
		t.Fatal("validateManifest accepted duplicate identifiers")
	}
	if err := validateManifest(Manifest{Version: 1, Items: twoPaths}); err == nil {
		t.Fatal("validateManifest accepted duplicate paths")
	}
	if err := validateManifest(Manifest{Version: 1, Items: []Item{{ID: goodID, RelativePath: "a.txt", SHA256: badSHA}}}); err == nil {
		t.Fatal("validateManifest accepted a non-hex checksum")
	}
	// The same guards run inside LoadManifest: a decryptable document with an
	// invalid item path is refused at load time.
	if err := os.WriteFile(filepath.Join(repository, manifestFile), forged(Manifest{Version: 1, SourceOS: "linux", Items: []Item{{ID: goodID, RelativePath: "../escape", SHA256: goodSHA}}}), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifest(repository, key); err == nil {
		t.Fatal("LoadManifest accepted a forged traversal item")
	}
	// A decryptable document that is not JSON is refused at decode time.
	if err := os.WriteFile(filepath.Join(repository, manifestFile), sealedPlain(key, []byte("not json")), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifest(repository, key); err == nil {
		t.Fatal("LoadManifest accepted non-JSON ciphertext")
	}
}

// sealedPlain encrypts arbitrary plaintext under the manifest context so
// tests can forge tamper-resistant but semantically invalid documents.
func sealedPlain(key, plaintext []byte) []byte {
	ciphertext, _ := encryptBytes(key, plaintext, []byte("manifest-v1"))
	return ciphertext
}

func TestVerifyAndVerifiedRows(t *testing.T) {
	repository, _, keyPath := firstCapturedRepository(t)
	key := readKeyOrFatal(t, keyPath)
	// Verified rows: decrypt every object into the destination first.
	manifest, err := LoadManifest(repository, key)
	if err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	for _, item := range manifest.Items {
		objectPath := filepath.Join(repository, objectDir, item.ID+".bin")
		target := filepath.Join(destination, filepath.FromSlash(item.RelativePath))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := decryptFile(objectPath, target, key, item.ID, item.SHA256, item.Size); err != nil {
			t.Fatal(err)
		}
	}
	results, err := VerifyDestination(repository, destination, key)
	if err != nil {
		t.Fatal(err)
	}
	if !reportHasStatusKind(results, "file", "verified") {
		t.Fatalf("verified rows missing: %#v", results)
	}
	if err := Verify(repository, key); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyDestination(repository, t.TempDir(), bytes.Repeat([]byte{66}, KeySize)); err == nil {
		t.Fatal("VerifyDestination accepted a wrong key")
	}
	if err := Verify(repository, bytes.Repeat([]byte{66}, KeySize)); err == nil {
		t.Fatal("Verify accepted a wrong key")
	}
	// A properly sealed object with the wrong plaintext is a checksum miss.
	first := manifest.Items[0]
	wrongContent, err := encryptBytes(key, []byte("different"), chunkAAD(first.ID, 0))
	if err != nil {
		t.Fatal(err)
	}
	object := &bytes.Buffer{}
	object.WriteString("MKF1")
	if err := writeChunk(object, wrongContent); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, objectDir, first.ID+".bin"), object.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Verify(repository, key); err == nil {
		t.Fatal("Verify accepted a mismatched object")
	}
}

// ---------------------------------------------------------------------------

func TestIsCredentialLikeTable(t *testing.T) {
	for path, want := range map[string]bool{
		".ssh":                            true,
		"work/.ssh/id_rsa":                true,
		"outbox/.gnupg/pubring.kbx":       true,
		".aws/credentials":                true,
		".azure/myconfig":                 true,
		".config/gcloud/access_tokens.db": true,
		"Keychain/db":                     true,
		"Login Data":                      true,
		"Cookies":                         true,
		"id_ed25519":                      true,
		"github-token.txt":                true,
		"my-credentials.json":             true,
		".config/opencode/auth.json":      true,
		"documents/report.pdf":            false,
		"photos/me.png":                   false,
		"tokenized-name-check.mkv":        true,
	} {
		if got := isCredentialLike(filepath.FromSlash(path)); got != want {
			t.Fatalf("isCredentialLike(%q) = %v, want %v", path, got, want)
		}
	}
	for path, want := range map[string]bool{
		"opencode/auth.json":         true,
		"shared/opencode/auth.json":  true,
		".config/opencode/auth.json": true,
		"opencode/auth.json.nightly": true,
		"auth.json":                  false,
		"opencode/config.json":       false,
		"oidc-agent/auth.json":       false,
	} {
		if got := isPortableCredentialPath(filepath.FromSlash(path)); got != want {
			t.Fatalf("isPortableCredentialPath(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestReparseSeamFallbacks(t *testing.T) {
	// An unparseable path, a missing path, and a plain directory all answer
	// "no reparse point"; Windows builtins and stubs agree.
	if hasReparsePoint(filepath.Join(t.TempDir(), "missing")) {
		t.Fatal("a missing path cannot carry reparse metadata")
	}
	if hasReparsePoint(t.TempDir()) {
		t.Fatal("a plain directory must not be treated as a reparse point")
	}
	if hasReparsePoint("with\x00nul-byte") {
		t.Fatal("an unperformable lookup must not report reparse metadata")
	}
}

func TestReportSeqWriter(t *testing.T) {
	report := NewReport("restore")
	report.Add(Result{Kind: "file", ID: "x", Status: "verified"})
	report.Finish()
	if err := WriteHumanReport(&secondWriteFails{}, report); err == nil {
		t.Fatal("the mid-report write failure must surface")
	}
}

type secondWriteFails struct {
	count int
}

func (writer *secondWriteFails) Write(p []byte) (int, error) {
	writer.count++
	if writer.count == 2 {
		return 0, errors.New("stream died mid-report")
	}
	return len(p), nil
}

func TestPlanApplicationsNativeBranch(t *testing.T) {
	plans := PlanApplications("windows", "windows", []Application{{Manager: "winget", Identifier: "Git.Git"}})
	if len(plans) != 1 || plans[0].Status != "installable" || plans[0].Target.Identifier != "Git.Git" {
		t.Fatalf("same-platform plan wrong: %#v", plans)
	}
	plans = PlanApplications("windows", "plan9", []Application{{Manager: "winget", Identifier: "x"}})
	if len(plans) != 1 || plans[0].Status != "unsupported" {
		t.Fatalf("unsupported destination plan wrong: %#v", plans)
	}
	plans = PlanApplications("linux", "linux", []Application{{Manager: "", Identifier: "x"}})
	if len(plans) != 1 || plans[0].Status != "installable" {
		t.Fatalf("same-platform empty-manager plan wrong: %#v", plans)
	}
}

func TestCaptureWalkExclusionReasons(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gitconfig"), []byte("[user]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "secret-token.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".ssh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".ssh", "id_rsa"), []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	items, exclusions, err := inventoryWithOptions(root, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("everything should be excluded: %#v", items)
	}
	reasons := map[string]string{}
	for _, exclusion := range exclusions {
		reasons[exclusion.RelativePath] = exclusion.Reason
	}
	if reasons[".gitconfig"] != "handled by Git settings recipe" {
		t.Fatalf("gitconfig exclusion reason wrong: %#v", reasons)
	}
	if reasons["secret-token.txt"] != "credential-like path" {
		t.Fatalf("credential exclusion reason wrong: %#v", reasons)
	}
	// Policy walks stop at a credential directory itself, so the reason is
	// recorded against ".ssh" and isCredentialLike catches both.
	if reasons[filepath.FromSlash(".ssh")] != "credential-like path" {
		t.Fatalf("ssh exclusion reason wrong: %#v", reasons)
	}
}

func TestCaptureSkipsDirectoryPolicyRecursive(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "keep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "AppData", "Local", "Temp", "deeper"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AppData", "Local", "Temp", "deeper", "junk"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	items, exclusions, err := inventoryWithOptions(root, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].RelativePath != "keep.txt" {
		t.Fatalf("policy directories must be skipped wholesale: %#v", items)
	}
	if len(exclusions) == 0 {
		t.Fatal("policy exclusions missing")
	}
}

func TestCaptureFakeEntryDecisions(t *testing.T) {
	root := t.TempDir()
	repository := t.TempDir()

	// Symlink-typed entries are excluded.
	symlinkEntry := fakeDirEntry{name: "link", typ: os.ModeSymlink}
	outcome, err := classifyWalkEntry(root, repository, false, filepath.Join(root, "link"), symlinkEntry, nil)
	if err != nil || outcome.exclusion == nil || outcome.exclusion.Reason != "symbolic link" {
		t.Fatalf("symlink entry: %#v %v", outcome.exclusion, err)
	}
	// Non-regular files are excluded.
	irregular := fakeDirEntry{name: "fifo", typ: os.ModeNamedPipe, info: fakeFileInfo{name: "fifo", mode: os.ModeNamedPipe}}
	outcome, err = classifyWalkEntry(root, repository, false, filepath.Join(root, "fifo"), irregular, nil)
	if err != nil || outcome.exclusion == nil || outcome.exclusion.Reason != "unsupported file type" {
		t.Fatalf("irregular entry: %#v %v", outcome.exclusion, err)
	}
	// Info failures are recorded as unreadable.
	broken := fakeDirEntry{name: "broken", infoErr: errors.New("stat failed")}
	outcome, err = classifyWalkEntry(root, repository, false, filepath.Join(root, "broken"), broken, nil)
	if err != nil || outcome.exclusion == nil || outcome.exclusion.Reason != "unreadable" {
		t.Fatalf("broken entry: %#v %v", outcome.exclusion, err)
	}
	// A walk error below the root is recorded, not fatal.
	outcome, err = classifyWalkEntry(root, repository, false, filepath.Join(root, "insane"), broken, errors.New("access denied"))
	if err != nil || outcome.exclusion == nil || outcome.exclusion.Reason != "unreadable" {
		t.Fatalf("nested walk error: %#v %v", outcome.exclusion, err)
	}
	// A path outside the source root cannot be relativised. Rel rejects
	// rooted-path/unrooted-root mixes on every platform, so driving it needs
	// no filesystem staging.
	foreign := filepath.Join(t.TempDir(), "elsewhere")
	if _, err = classifyWalkEntry("source-root", repository, false, foreign, broken, nil); err == nil {
		t.Fatal("a foreign path must fail the inventory")
	}
	// The source root itself being unreadable kills the inventory.
	if _, _, err = inventoryWithOptions(filepath.Join(root, "missing"), repository, false); err == nil {
		t.Fatal("a missing source root must fail the inventory")
	}
}

type fakeDirEntry struct {
	name    string
	isDir   bool
	typ     os.FileMode
	info    fs.FileInfo
	infoErr error
}

func (entry fakeDirEntry) Name() string               { return entry.name }
func (entry fakeDirEntry) IsDir() bool                { return entry.isDir }
func (entry fakeDirEntry) Type() fs.FileMode          { return entry.typ }
func (entry fakeDirEntry) Info() (fs.FileInfo, error) { return entry.info, entry.infoErr }

type fakeFileInfo struct {
	name string
	size int64
	mode fs.FileMode
	time time.Time
}

func (info fakeFileInfo) Name() string       { return info.name }
func (info fakeFileInfo) Size() int64        { return info.size }
func (info fakeFileInfo) Mode() fs.FileMode  { return info.mode }
func (info fakeFileInfo) ModTime() time.Time { return info.time }
func (info fakeFileInfo) IsDir() bool        { return info.mode.IsDir() }
func (info fakeFileInfo) Sys() interface{}   { return nil }

func TestRestoreWithOptionsFaults(t *testing.T) {
	repository, _, keyPath := firstCapturedRepository(t)
	key := readKeyOrFatal(t, keyPath)
	// RestoreWithOptions only opens the install workspace when the manifest
	// carries applications, so add one first.
	addApplicationToManifest(t, repository, key, "", []Application{{Manager: "winget", Identifier: "Git.Git"}})
	destination := t.TempDir()
	seamOverride(t, &osMkdirTemp, func(string, string) (string, error) { return "", errors.New("no workspace") })
	if err := RestoreWithOptions(repository, destination, key, false, false, true, stubRunner(nil, nil)); err == nil {
		t.Fatal("restore surfaced the workspace failure")
	}
}

func TestLoadRestoreStateCraftedBranches(t *testing.T) {
	repository := t.TempDir()
	key := bytes.Repeat([]byte{68}, KeySize)
	save := func(payload []byte) {
		t.Helper()
		ciphertext, err := encryptBytes(key, payload, []byte("restore-state-v1"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repository, restoreStateFile), ciphertext, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Undecodable payload.
	save([]byte("{not-json"))
	if _, err := LoadRestoreState(repository, key); err == nil {
		t.Fatal("garbage state accepted")
	}
	// Unsupported version.
	save([]byte(`{"version":9}`))
	if _, err := LoadRestoreState(repository, key); err == nil {
		t.Fatal("state version 9 accepted")
	}
	// A state without items gets the empty map.
	save([]byte(`{"version":1,"dry_run_completed":true,"plan_fingerprint":"p","bundle_fingerprint":"b","destination":"d"}`))
	state, err := LoadRestoreState(repository, key)
	if err != nil || !state.DryRunCompleted || state.Items == nil {
		t.Fatalf("items default wrong: %#v %v", state, err)
	}
}
