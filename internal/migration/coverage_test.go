package migration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Test infrastructure

// stubRunner answers every command identically; use it to exercise provider
// paths without running real package managers.
func stubRunner(output []byte, err error) fakeRunner {
	return func(name string, args ...string) ([]byte, error) { return output, err }
}

type fakeRunner func(name string, args ...string) ([]byte, error)

func (f fakeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return f(name, args...)
}

// fakeRunnerWriter behaves like the real exporters: when the command asks for
// an output file it materialises canned content there, and its stdout carries
// the same content (Linux package listings arrive on stdout).
func fakeRunnerWriter(content string, errs ...error) fakeRunner {
	return fakeRunner(func(name string, args ...string) ([]byte, error) {
		for _, failure := range errs {
			if failure != nil {
				return nil, failure
			}
		}
		for index, arg := range args {
			if (arg == "--output" || arg == "--file") && index+1 < len(args) {
				if err := os.WriteFile(args[index+1], []byte(content), 0o600); err != nil {
					return nil, err
				}
			}
		}
		return []byte(content), nil
	})
}

// overridePlatform swaps the platform seam; callers must defer cleanup().
func overridePlatform(osName string) func() {
	saved := goosFunc
	goosFunc = func() string { return osName }
	return func() { goosFunc = saved }
}

func overrideLookPath(available []string, errorsFor ...string) func() {
	saved := platformLookPath
	platformLookPath = func(name string) (string, error) {
		for _, failure := range errorsFor {
			if failure == name {
				return "", errors.New("not available: " + name)
			}
		}
		for _, tool := range available {
			if tool == name {
				return "/fake/" + name, nil
			}
		}
		return "", errors.New("not available: " + name)
	}
	return func() { platformLookPath = saved }
}

// firstCapturedRepository builds a tiny two-file repository used by several
// tests.
func firstCapturedRepository(t *testing.T) (repository, destination, keyPath string) {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "src")
	repository = filepath.Join(root, "repo")
	destination = filepath.Join(root, "dst")
	keyPath = filepath.Join(root, "key")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, bytes.Repeat([]byte{37}, KeySize), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "a.txt"), []byte("aaa"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "z.txt"), []byte("zzz"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Capture(source, repository, readKeyOrFatal(t, keyPath)); err != nil {
		t.Fatal(err)
	}
	return repository, destination, keyPath
}

func readKeyOrFatal(t *testing.T, keyPath string) []byte {
	t.Helper()
	key, err := ReadKey(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// ---------------------------------------------------------------------------

func TestDoctorFullMatrix(t *testing.T) {
	root := t.TempDir()
	source := t.TempDir()
	repository := filepath.Join(root, "repo")
	destination := filepath.Join(root, "destination")
	keyPath := filepath.Join(root, "key")
	key := bytes.Repeat([]byte{41}, KeySize)
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Capture(source, repository, key); err != nil {
		t.Fatal(err)
	}

	// Empty arguments: single invalid bundle finding.
	if got := Doctor("", "", ""); len(got) != 1 || got[0].Kind != "bundle" || got[0].Status != "invalid" {
		t.Fatalf("doctor empty arguments: %#v", got)
	}
	// Missing repository directory.
	if got := Doctor(filepath.Join(root, "nope"), "", ""); got[0].Status != "invalid" {
		t.Fatalf("doctor missing bundle: %#v", got)
	}
	// Present bundle, missing key, missing destination.
	got := Doctor(repository, filepath.Join(root, "missing.key"), destination)
	statuses := map[string]string{}
	for _, result := range got {
		statuses[result.Kind] = result.Status
	}
	if statuses["bundle"] != "available" || statuses["key"] != "missing" || statuses["destination"] != "missing" {
		t.Fatalf("doctor matrix wrong: %#v", got)
	}
	if _, seen := statuses["manifest"]; seen {
		t.Fatal("manifest should not be verified without a working key")
	}
	// Good key, existing destination directory: everything verified.
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	got = Doctor(repository, keyPath, destination)
	statuses = map[string]string{}
	for _, result := range got {
		statuses[result.Kind] = result.Status
	}
	if statuses["key"] != "available" || statuses["manifest"] != "verified" || statuses["destination"] != "available" {
		t.Fatalf("doctor happy path wrong: %#v", got)
	}
	// Wrong key: manifest unreadable.
	wrongKeyPath := filepath.Join(root, "wrong.key")
	if err := os.WriteFile(wrongKeyPath, bytes.Repeat([]byte{42}, KeySize), 0o600); err != nil {
		t.Fatal(err)
	}
	if !doctorHasStatus(Doctor(repository, wrongKeyPath, ""), "bundle", "unreadable") {
		t.Fatalf("wrong key not reported: %#v", Doctor(repository, wrongKeyPath, ""))
	}
	// Destination occupied by a file is invalid.
	fileDestination := filepath.Join(root, "destination-file")
	if err := os.WriteFile(fileDestination, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !doctorHasStatus(Doctor(repository, keyPath, fileDestination), "destination", "invalid") {
		t.Fatalf("file destination not flagged: %#v", Doctor(repository, keyPath, fileDestination))
	}
	// Corrupt object: bundle corrupt.
	objects, err := os.ReadDir(filepath.Join(repository, objectDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) == 0 {
		t.Fatal("no object files to corrupt")
	}
	if err := os.WriteFile(filepath.Join(repository, objectDir, objects[0].Name()), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !doctorHasStatus(Doctor(repository, keyPath, ""), "bundle", "corrupt") {
		t.Fatalf("corrupt object not reported: %#v", Doctor(repository, keyPath, ""))
	}
}

func doctorHasStatus(results []Result, kind, status string) bool {
	for _, result := range results {
		if result.Kind == kind && result.Status == status {
			return true
		}
	}
	return false
}

func TestMiscHelpers(t *testing.T) {
	if !allManager([]Application{{Manager: "winget"}}, "winget") {
		t.Fatal("allManager must accept a uniform list")
	}
	if allManager([]Application{{Manager: "winget"}, {Manager: "brew"}}, "winget") {
		t.Fatal("allManager must reject a mixed list")
	}
	if !allManager(nil, "winget") {
		t.Fatal("allManager must treat an empty list as uniform")
	}
	if errorString(nil) != "" {
		t.Fatal("errorString(nil) must be empty")
	}
	if errorString(errors.New("boom")) != "boom" {
		t.Fatal("errorString must stringify errors")
	}
	source := t.TempDir()
	repository := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	items, _, err := inventory(source, repository)
	if err != nil || len(items) != 1 {
		t.Fatalf("inventory wrapper: %v %#v", err, items)
	}
	// isWithin(candidate, root).
	if isWithin("c1/foo", "c1/foobar") {
		t.Fatal("prefix boundary must not count as within")
	}
	if !isWithin(filepath.Join("c1", "foo", "bar"), filepath.Join("c1", "foo")) {
		t.Fatal("nested candidate must be within")
	}
}

func TestRestoreWithOptionsDryRunAndApps(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "src")
	repository := filepath.Join(root, "repo")
	destination := filepath.Join(root, "dst")
	keyPath := filepath.Join(root, "key")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{37}, KeySize)
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"a.txt": "aaa", "z.txt": "zzz"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cleanup := overridePlatform("windows")
	defer cleanup()
	restoreLookPath := overrideLookPath([]string{"winget"})
	defer restoreLookPath()

	export := `{"Sources":[{"Packages":[{"PackageIdentifier":"Git.Git"}]}]}`
	if _, err := CaptureWithOptions(source, repository, key, true, false, fakeRunnerWriter(export)); err != nil {
		t.Fatalf("capture with applications: %v", err)
	}
	if _, err := DryRunRestore(repository, destination, keyPath, "personal", false); err != nil {
		t.Fatal(err)
	}
	// Dry-run must not write anything at the destination.
	if err := RestoreWithOptions(repository, destination, key, true, true, false, stubRunner(nil, nil)); err != nil {
		t.Fatalf("dry-run restore: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "a.txt")); !errIsNotExist(err) {
		t.Fatal("dry-run restore wrote destination files")
	}
	// installApplications with a fake runner still copies the files.
	if err := RestoreWithOptions(repository, destination, key, false, false, true, stubRunner(nil, nil)); err != nil {
		t.Fatalf("restore with applications: %v", err)
	}
	for _, name := range []string{"a.txt", "z.txt"} {
		if _, err := os.Stat(filepath.Join(destination, name)); err != nil {
			t.Fatalf("restore did not copy %s: %v", name, err)
		}
	}
	// A failing install runner surfaces only when there is something to install.
	if err := RestoreWithOptions(repository, destination, key, false, false, true, stubRunner(nil, errors.New("winget down"))); err == nil {
		t.Fatal("restore accepted a failing install runner")
	}
}

// Restore without a prior dry-run must be refused.
func TestRestoreRequiresDryRunState(t *testing.T) {
	repository, destination, keyPath := firstCapturedRepository(t)
	if _, err := ManagedRestore(repository, destination, keyPath, "personal", false, false); err == nil {
		t.Fatal("restore without a completed dry-run must fail")
	}
}

func TestResumeAfterFailedRestoreRepairsAndResumes(t *testing.T) {
	repository, destination, keyPath := firstCapturedRepository(t)
	key := readKeyOrFatal(t, keyPath)
	if _, err := DryRunRestore(repository, destination, keyPath, "personal", false); err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadManifest(repository, key)
	if err != nil {
		t.Fatal(err)
	}
	var zObject string
	for _, item := range manifest.Items {
		if item.RelativePath == "z.txt" {
			zObject = filepath.Join(repository, objectDir, item.ID+".bin")
		}
	}
	if zObject == "" {
		t.Fatal("z.txt object not found")
	}
	backup, err := os.ReadFile(zObject)
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt the last object so restore fails midway on the second copy.
	if err := os.WriteFile(zObject, []byte{0}, 0o600); err != nil {
		t.Fatal(err)
	}
	_, restoreErr := ManagedRestore(repository, destination, keyPath, "personal", false, false)
	if restoreErr == nil {
		t.Fatal("restore had to fail on the corrupted object")
	}
	if _, err := os.Stat(filepath.Join(destination, "a.txt")); err != nil {
		t.Fatalf("first file should already be copied before the failure: %v", err)
	}
	// Repair the object from the backup and resume.
	if err := os.WriteFile(zObject, backup, 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := ResumeRestore(repository, keyPath, "personal", false)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	statuses := map[string]int{}
	for _, result := range report.Results {
		statuses[result.Status]++
	}
	if statuses["already-complete"] != 1 || statuses["copied"] != 1 {
		t.Fatalf("resume report wrong: %#v", report.Results)
	}
	content, err := os.ReadFile(filepath.Join(destination, "z.txt"))
	if err != nil || string(content) != "zzz" {
		t.Fatalf("resumed file wrong: %q %v", content, err)
	}
}

func TestVerifyDestinationBranches(t *testing.T) {
	repository, destination, keyPath := firstCapturedRepository(t)
	key := readKeyOrFatal(t, keyPath)

	// Everything missing.
	for _, result := range mustVerifyDestination(t, repository, destination, key) {
		if result.Status != "missing" {
			t.Fatalf("expected missing rows: %#v", result)
		}
	}
	// A directory where a file should be is unreadable.
	bGood := filepath.Join(destination, "z.txt")
	if err := os.MkdirAll(bGood, 0o755); err != nil {
		t.Fatal(err)
	}
	if !verifyHasStatus(mustVerifyDestination(t, repository, destination, key)[:], "unreadable") {
		t.Fatal("unreadable row missing")
	}
	// Tampered contents are checksum mismatches.
	if err := os.Remove(bGood); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bGood, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !verifyHasStatus(mustVerifyDestination(t, repository, destination, key), "checksum-mismatch") {
		t.Fatal("mismatch row missing")
	}

	// Escape branch: a manifest item whose joined path leaves the destination.
	// validateManifest only rejects a leading ../, so the traversal hides
	// behind a real directory component; VerifyDestination must still refuse.
	forged := Manifest{
		Version: 1, SourceOS: runtime.GOOS, SourceRoot: "src",
		Items: []Item{{
			ID:           strings.Repeat("ab", 32),
			RelativePath: "Documents/../../escape.txt",
			Size:         0,
			SHA256:       strings.Repeat("cd", 32),
		}},
	}
	encoded, err := json.Marshal(forged)
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
	if _, err := VerifyDestination(repository, destination, key); err == nil {
		t.Fatal("VerifyDestination accepted an escaping item")
	}
	if _, err := PlanRestore(forged, destination, false); err == nil {
		t.Fatal("PlanRestore accepted an escaping item")
	}
}

func mustVerifyDestination(t *testing.T, repository, destination string, key []byte) []Result {
	t.Helper()
	results, err := VerifyDestination(repository, destination, key)
	if err != nil {
		t.Fatal(err)
	}
	return results
}

func verifyHasStatus(results []Result, status string) bool {
	for _, result := range results {
		if result.Status == status {
			return true
		}
	}
	return false
}

// PlanRestore refuses destinations touched by symlinks and targets the
// platform cannot even stat.
func TestPlanRestoreGuards(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	destination := filepath.Join(root, "dest")

	// Parent escapes the restore root.
	if err := rejectSymlinkedParents(filepath.Join(outside, "x.txt"), destination); err == nil {
		t.Fatal("target outside the root must be refused")
	}
	// An unresolvable absolute path (name with a NUL byte).
	if err := rejectSymlinkedParents(filepath.Join(root, "bad\x00name"), root); err == nil {
		t.Fatal("unstatable target must be refused")
	}
	// A name the platform refuses to stat directly (reserved pipe/colon
	// syntax only exists on Windows; on POSIX it is an ordinary name).
	if runtime.GOOS == "windows" {
		if err := rejectSymlinkedParents(filepath.Join(root, "bad|name"), root); err == nil {
			t.Fatal("reserved-name target must be refused")
		}
	}
	// A symlinked parent (junction on Windows) stops the restore.
	_ = os.MkdirAll(destination, 0o755)
	link, ok := tryCreateDirectoryLink(t, filepath.Join(destination, "link"), root)
	if ok {
		if err := rejectSymlinkedParents(filepath.Join(link, "x.txt"), destination); err == nil {
			t.Fatal("symlinked destination must be refused")
		}
		_ = os.Remove(link)
	} else {
		t.Log("junction/symlink creation unavailable; symlink guard not exercised")
	}
	// Clean nested target: accepted.
	if err := rejectSymlinkedParents(filepath.Join(destination, "Documents", "x.txt"), destination); err != nil {
		t.Fatalf("clean target refused: %v", err)
	}

	// PlanRestore inspects each target; a stat failure short-circuits the plan.
	itemID := strings.Repeat("ab", 32)
	secondID := strings.Repeat("cd", 32)
	if runtime.GOOS == "windows" {
		invalid := Manifest{Version: 1, Items: []Item{{
			ID: itemID, RelativePath: "bad|name", SHA256: secondID,
		}}}
		if _, err := PlanRestore(invalid, root, false); err == nil {
			t.Fatal("PlanRestore accepted an unstatable target")
		}
	} else {
		// A file sitting where a parent directory should be.
		occupied := filepath.Join(root, "occupied")
		if err := os.WriteFile(occupied, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		invalid := Manifest{Version: 1, Items: []Item{{
			ID: itemID, RelativePath: "occupied/child", SHA256: secondID,
		}}}
		if _, err := PlanRestore(invalid, root, false); err == nil {
			t.Fatal("PlanRestore accepted a non-directory parent")
		}
	}
}

func tryCreateDirectoryLink(t *testing.T, link, target string) (string, bool) {
	t.Helper()
	_ = os.Remove(link)
	if err := os.Symlink(target, link); err == nil {
		return link, true
	}
	if runtime.GOOS == "windows" {
		if exec.Command("cmd", "/c", "mklink", "/J", link, target).Run() == nil {
			return link, true
		}
	}
	return "", false
}

func TestStateHelpers(t *testing.T) {
	manifest := Manifest{Version: 1, Items: []Item{{ID: freshItemID()}}}
	bundle := BundleFingerprint(manifest)
	plan, err := PlanFingerprint(manifest, "dst", "personal", false)
	if err != nil {
		t.Fatal(err)
	}
	state := RestoreState{BundleFingerprint: bundle, PlanFingerprint: plan}
	if err := requireMatchingDryRun(state, plan); err == nil {
		t.Fatal("a state without a completed dry-run must be refused")
	}
	state.DryRunCompleted = true
	if err := requireMatchingDryRun(state, "other"); err == nil {
		t.Fatal("fingerprint mismatch must be refused")
	}
	if err := requireMatchingDryRun(state, plan); err != nil {
		t.Fatal(err)
	}

	key := bytes.Repeat([]byte{45}, KeySize)
	repository := t.TempDir()
	if err := SaveRestoreState(repository, key, state); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadRestoreState(repository, key)
	if err != nil || !loaded.DryRunCompleted {
		t.Fatalf("state round trip: %v %#v", err, loaded)
	}
	if _, err := LoadRestoreState(repository, bytes.Repeat([]byte{46}, KeySize)); err == nil {
		t.Fatal("a wrong key must not decrypt the restore state")
	}
	if _, err := LoadRestoreState(filepath.Join(repository, "missing"), key); err == nil {
		t.Fatal("a missing restore state must fail")
	}
}

func freshItemID() string { return strings.Repeat("0123456789abcdef", 4) }

func TestReportWriters(t *testing.T) {
	report := NewReport("verify-destination")
	report.Add(Result{Kind: "file", ID: "x", Status: "verified"})
	report.Finish()
	var jsonBuffer bytes.Buffer
	if err := WriteJSONReport(&jsonBuffer, report); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(jsonBuffer.String(), `"command":"verify-destination"`) {
		t.Fatalf("json report wrong: %s", jsonBuffer.String())
	}
	var humanBuffer bytes.Buffer
	if err := WriteHumanReport(&humanBuffer, report); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(humanBuffer.String(), "file verified x") {
		t.Fatalf("human report wrong: %s", humanBuffer.String())
	}
	failing := failWriter{}
	if err := WriteJSONReport(failing, report); err == nil {
		t.Fatal("json writer errors must surface")
	}
	if err := WriteHumanReport(failing, report); err == nil {
		t.Fatal("human writer errors must surface")
	}
}

type failWriter struct{}

func (failWriter) Write(p []byte) (int, error) { return 0, errors.New("disk full") }

func TestPortableErrorPaths(t *testing.T) {
	// Empty temporary directory is not a bundle.
	if _, err := PackPortableDrive(t.TempDir(), "", ""); err == nil {
		t.Fatal("pack accepted an empty directory")
	}
	// A manifest without a format marker is not a repository.
	bundle := t.TempDir()
	if err := os.WriteFile(filepath.Join(bundle, manifestFile), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PackPortableDrive(bundle, "", ""); err == nil {
		t.Fatal("pack accepted a repository without a format marker")
	}

	repository := t.TempDir()
	keyPath := filepath.Join(t.TempDir(), "portable.key")
	key := bytes.Repeat([]byte{47}, KeySize)
	source := t.TempDir()
	if err := os.WriteFile(source+"/f.txt", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Capture(source, repository, key); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}
	// Unreadable key.
	if _, err := PackPortableDrive(repository, filepath.Join(repository, "missing.key"), ""); err == nil {
		t.Fatal("pack accepted a missing key file")
	}
	// First pack writes the drive key; identical reuse is permitted.
	if _, err := PackPortableDrive(repository, keyPath, ""); err != nil {
		t.Fatalf("first pack: %v", err)
	}
	if _, err := PackPortableDrive(repository, keyPath, ""); err != nil {
		t.Fatalf("pack with the same on-drive key: %v", err)
	}
	// A different key already on the drive is a conflict.
	otherKey := filepath.Join(t.TempDir(), "other.key")
	if err := os.WriteFile(otherKey, bytes.Repeat([]byte{48}, KeySize), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PackPortableDrive(repository, otherKey, ""); err == nil {
		t.Fatal("pack accepted a conflicting on-drive key")
	}

	// Binaries: regular files copy, directories and missing names are skipped.
	binaries := t.TempDir()
	if err := os.WriteFile(filepath.Join(binaries, portableBinaryNames[0]), []byte("binary-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(binaries, portableBinaryNames[1]), 0o755); err != nil {
		t.Fatal(err)
	}
	written, err := PackPortableDrive(repository, keyPath, binaries)
	if err != nil {
		t.Fatalf("pack with binaries: %v", err)
	}
	foundBinary := false
	for _, path := range written {
		if strings.HasSuffix(path, portableBinaryNames[0]) {
			foundBinary = true
		}
	}
	if !foundBinary {
		t.Fatalf("binary missing from the written list: %#v", written)
	}

	// copyPortableFile: missing source.
	if err := copyPortableFile(filepath.Join(binaries, "missing"), filepath.Join(t.TempDir(), "out"), 0); err == nil {
		t.Fatal("copy accepted a missing source")
	}
	// copyPortableFile: the temp sibling cannot be created.
	target := filepath.Join(t.TempDir(), "out")
	if err := os.MkdirAll(target+".tmp", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copyPortableFile(filepath.Join(binaries, portableBinaryNames[0]), target, 2); err == nil {
		t.Fatal("copy accepted a blocked target")
	}
	// copyPortableFile: the target itself cannot be replaced.
	blockedTarget := filepath.Join(binaries, "blocked")
	if err := os.MkdirAll(filepath.Join(blockedTarget, "occupied"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copyPortableFile(filepath.Join(binaries, portableBinaryNames[0]), blockedTarget, 2); err == nil {
		t.Fatal("copy accepted a blocked target directory")
	}
}

func TestChromeDarwinAndLinuxLayouts(t *testing.T) {
	if chromeUserDataDirForOS("darwin", "H") != filepath.Join("H", "Library", "Application Support", "Google", "Chrome") {
		t.Fatal("darwin chrome path wrong")
	}
	if chromeUserDataDirForOS("linux", "H") != filepath.Join("H", ".config", "google-chrome") {
		t.Fatal("linux chrome path wrong")
	}

	// Capture and restore through the darwin layout while running anywhere.
	cleanup := overridePlatform("darwin")
	defer cleanup()
	home := t.TempDir()
	profile := filepath.Join(home, "Library", "Application Support", "Google", "Chrome", "Default")
	if err := os.MkdirAll(profile, 0o755); err != nil {
		t.Fatal(err)
	}
	bookmarks := `{"roots":{},"version":1}`
	if err := os.WriteFile(filepath.Join(profile, "Bookmarks"), []byte(bookmarks), 0o644); err != nil {
		t.Fatal(err)
	}
	// A malformed sibling may not crash the capture.
	if err := os.WriteFile(filepath.Join(profile, "Bookmarks.bak"), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	setting, checklist, err := captureChromeSetting(home)
	if err != nil {
		t.Fatal(err)
	}
	if setting.Values[chromeBookmarksFile] != bookmarks {
		t.Fatalf("darwin chrome bookmarks wrong: %#v", setting.Values)
	}
	if len(checklist) == 0 {
		t.Fatal("chrome capture must always emit its sign-in checklist")
	}

	destination := t.TempDir()
	restore := NewReport("restore")
	if err := restoreSettings(Manifest{Settings: []Setting{setting}}, destination, &restore, false, false, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(chromeUserDataDirForOS("darwin", destination), "Default", "Bookmarks")); err != nil {
		t.Fatal(err)
	}
}

func TestGitTransformDarwinDestination(t *testing.T) {
	setting := Setting{Recipe: "git-v1", Values: map[string]string{"name": "n", "email": "e", "autocrlf": "true"}}
	content, err := RenderGitSetting(setting, "windows", "darwin")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "autocrlf = input") {
		t.Fatalf("darwin transform wrong: %s", content)
	}
	setting.Values["autocrlf"] = "input"
	content, err = RenderGitSetting(setting, "linux", "windows")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "autocrlf = true") {
		t.Fatalf("windows transform wrong: %s", content)
	}
	if _, err := RenderGitSetting(Setting{Recipe: "wrong"}, "linux", "linux"); err == nil {
		t.Fatal("foreign recipe accepted")
	}
	if _, err := RenderGitSetting(Setting{Recipe: "git-v1", Values: map[string]string{"evil": "1"}}, "linux", "linux"); err == nil {
		t.Fatal("foreign value accepted")
	}
	if content, err := RenderGitSetting(Setting{Recipe: "git-v1", Values: map[string]string{"alias.st": "status", "defaultbranch": "main"}}, "darwin", "darwin"); err != nil || !strings.Contains(string(content), "defaultBranch = main") || !strings.Contains(string(content), "st = status") {
		t.Fatalf("alias/default branch render wrong: %s %v", content, err)
	}
}

func TestSSHRenderErrors(t *testing.T) {
	if _, err := RenderSSHConfig(Setting{Recipe: "not-ssh"}); err == nil {
		t.Fatal("foreign recipe accepted")
	}
	if _, err := RenderSSHConfig(Setting{Recipe: "ssh-config-v1", Values: map[string]string{"config": "a", "extra": "b"}}); err == nil {
		t.Fatal("extra values accepted")
	}
}

func TestVSCodeExtensionInstallPaths(t *testing.T) {
	restoreLookPath := overrideLookPath([]string{})
	defer restoreLookPath()

	// Without the CLI on PATH the extensions are reported as skipped.
	report := NewReport("restore")
	installVSCodeExtensions(&report, []string{"golang.go"}, stubRunner(nil, nil))
	if !verifyHasStatus(report.Results, "skipped") {
		t.Fatalf("expected skipped rows: %#v", report.Results)
	}
	// With the CLI present, installs succeed and failures are reported.
	restoreLookPath = overrideLookPath([]string{"code"})
	defer restoreLookPath()
	report = NewReport("restore")
	installVSCodeExtensions(&report, []string{"golang.go", "hashicorp.terraform"}, stubRunner(nil, nil))
	installed := 0
	for _, result := range report.Results {
		if result.Status == "installed" {
			installed++
		}
	}
	if installed != 2 {
		t.Fatalf("install rows wrong: %#v", report.Results)
	}
	report = NewReport("restore")
	installVSCodeExtensions(&report, []string{"golang.go"}, stubRunner(nil, errors.New("code failed")))
	if !verifyHasStatus(report.Results, "failed") {
		t.Fatalf("expected failed rows: %#v", report.Results)
	}
}

func TestVSCodeSettingsPathOnDarwinAndUnsupportedPlatforms(t *testing.T) {
	cleanup := overridePlatform("darwin")
	defer cleanup()
	path, err := vscodeSettingsPath("darwin")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, filepath.Join("Code", "User", "settings.json")) {
		t.Fatalf("darwin vscode path wrong: %s", path)
	}
	if _, err := vscodeSettingsPath("plan9"); err == nil {
		t.Fatal("unsupported platform accepted")
	}
}

func TestDevEnvPlatformSkipRecheckAndUnparsable(t *testing.T) {
	// Windows-only entries are platform-skipped on darwin.
	cleanup := overridePlatform("darwin")
	defer cleanup()
	report := NewReport("restore")
	setting := Setting{Recipe: "terminal-v1", Values: map[string]string{"wt-packaged": "{}"}}
	if err := restoreCatalogSetting(setting, t.TempDir(), &report, false); err != nil {
		t.Fatal(err)
	}
	if !verifyHasStatus(report.Results, "platform-skipped") {
		t.Fatalf("platform-skipped branch missing: %#v", report.Results)
	}
	cleanup()
	// A stored recipe value is re-scrubbed at restore time; any credential
	// that survived an old bundle is dropped again and reported.
	destination := t.TempDir()
	recheck := NewReport("restore")
	secretSetting := Setting{Recipe: "dev-v1", Values: map[string]string{".npmrc": "registry=https://registry.npmjs.org/\n_authToken=ghp_" + strings.Repeat("A", 24) + "\n"}}
	if err := restoreCatalogSetting(secretSetting, destination, &recheck, false); err != nil {
		t.Fatal(err)
	}
	if !verifyHasStatus(recheck.Results, "re-checked") {
		t.Fatalf("re-checked branch missing: %#v", recheck.Results)
	}
	written, err := os.ReadFile(filepath.Join(destination, ".npmrc"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(written), "ghp_") {
		t.Fatalf("credential survived re-scrub: %s", written)
	}
	// Unparsable files are excluded, never carried: the recipe itself is
	// omitted from the captured manifest.
	skip := overridePlatform("linux")
	defer skip()
	home := t.TempDir()
	writeDevFixture(t, home, ".docker/config.json", "{oops")
	settings, _, err := captureDevSettings(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, captured := range settings {
		if captured.Recipe == "docker-v1" && len(captured.Values) > 0 {
			t.Fatalf("unparsable file was still captured: %#v", captured.Values)
		}
	}
}

func TestLinuxInstallCommandTable(t *testing.T) {
	for manager, flag := range map[string]string{
		"apt": "-y", "dnf": "-y", "pacman": "--noconfirm", "zypper": "--non-interactive",
	} {
		command, ok := LinuxInstallCommand(Application{Manager: manager, Identifier: "pkg"})
		if !ok || !containsString(command.Args, flag) || !containsString(command.Args, "install") {
			t.Fatalf("manager %s wrong: %#v", manager, command)
		}
	}
	command, ok := LinuxInstallCommand(Application{Manager: "apk", Identifier: "pkg"})
	if !ok || !containsString(command.Args, "--no-interactive") || !containsString(command.Args, "add") {
		t.Fatalf("apk command wrong: %#v", command)
	}
	command, ok = LinuxInstallCommand(Application{Manager: "flatpak", Identifier: "pkg"})
	if !ok || !containsString(command.Args, "install") {
		t.Fatalf("flatpak command wrong: %#v", command)
	}
	command, ok = LinuxInstallCommand(Application{Manager: "snap", Identifier: "pkg"})
	if !ok || len(command.Args) == 0 || command.Args[0] != "install" {
		t.Fatalf("snap command wrong: %#v", command)
	}
	if _, ok := LinuxInstallCommand(Application{Manager: "apk"}); ok {
		t.Fatal("empty identifier accepted")
	}
	if _, ok := LinuxInstallCommand(Application{Manager: "flying-carpet", Identifier: "x"}); ok {
		t.Fatal("unknown manager accepted")
	}
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func TestInstallApplicationManagers(t *testing.T) {
	ctx := context.Background()
	runner := stubRunner(nil, nil)
	for _, manager := range []string{"apt", "dnf", "pacman", "zypper", "apk", "flatpak", "snap", "brew-brew", "brew-cask", "mas"} {
		if err := InstallApplication(ctx, runner, Application{Manager: manager, Identifier: "pkg"}); err != nil {
			t.Fatalf("manager %s: %v", manager, err)
		}
	}
	// The linux manager defers to whatever native manager is installed.
	restoreLookPath := overrideLookPath([]string{"apt"}, "dnf", "pacman", "zypper", "apk", "flatpak", "snap")
	defer restoreLookPath()
	if err := InstallApplication(ctx, runner, Application{Manager: "linux", Identifier: "pkg"}); err != nil {
		t.Fatal(err)
	}
	// No native manager: refused (own override, since containers may have a
	// real apt present).
	noManagersCleanup := overrideLookPath([]string{})
	if err := InstallApplication(ctx, runner, Application{Manager: "linux", Identifier: "pkg"}); err == nil {
		noManagersCleanup()
		t.Fatal("linux install without a native manager must fail")
	}
	noManagersCleanup()
	// Unknown managers fail closed.
	if err := InstallApplication(ctx, runner, Application{Manager: "flying-carpet", Identifier: "x"}); err == nil {
		t.Fatal("unsupported manager accepted")
	}
}

func TestInventoryDarwinApplications(t *testing.T) {
	clean := "brew git\n# comment\ncask visual-studio-code\ntap homebrew/core\n"
	applications, err := runInventory(t, "darwin", []string{"brew"}, fakeRunnerWriter(clean))
	if err != nil || len(applications) != 2 {
		t.Fatalf("darwin inventory: %v %#v", err, applications)
	}
	if applications[0].Manager != "brew-brew" || applications[1].Manager != "brew-cask" {
		t.Fatalf("darwin managers wrong: %#v", applications)
	}
	// brew missing fails, unsupported platform fails.
	if _, err := runInventory(t, "darwin", nil, fakeRunnerWriter(clean)); err == nil {
		t.Fatal("darwin inventory without brew must fail")
	}
	if _, err := runInventory(t, "plan9", nil, nil); err == nil {
		t.Fatal("unsupported platform accepted")
	}
	// Unsupported lines do not discard the rest.
	messy := "brew git\nbrew \"\"\nvscode golang.go\n"
	apps, err := runInventory(t, "darwin", []string{"brew"}, fakeRunnerWriter(messy))
	if err == nil {
		t.Fatal("Brewfile with unsupported lines must report them")
	}
	if len(apps) != 1 {
		t.Fatalf("valid intents lost: %#v", apps)
	}
}

func TestInventoryLinuxApplications(t *testing.T) {
	apps, err := runInventory(t, "linux", []string{"apk"}, fakeRunnerWriter("git\npython3\n"))
	if err != nil || len(apps) != 2 || apps[0].Manager != "apk" {
		t.Fatalf("linux inventory: %v %#v", err, apps)
	}
	// A manager whose export fails is skipped; other managers still count.
	multi := func(name string, args ...string) ([]byte, error) {
		if name == "flatpak" {
			return nil, errors.New("flatpak down")
		}
		return []byte("git\npython3\n"), nil
	}
	apps, err = runInventory(t, "linux", []string{"flatpak", "apk"}, multi)
	if err != nil || len(apps) != 2 {
		t.Fatalf("linux inventory with one failing manager: %v %#v", err, apps)
	}
	// Only one manager available: detected but empty listing is an error.
	if _, err := runInventory(t, "linux", []string{"apk"}, stubRunner(nil, nil)); err == nil {
		t.Fatal("empty listing must fail")
	}
	// Nothing available at all.
	if _, err := runInventory(t, "linux", nil, stubRunner(nil, nil)); err == nil {
		t.Fatal("no managers must fail")
	}
}

func TestParseLinuxPackagesShapes(t *testing.T) {
	// Repository part is split off the identifier for non-flatpak managers.
	apps := ParseLinuxPackages("apt", "favor/foo\nListing...\nbar\n")
	if len(apps) != 2 || apps[0].Identifier != "favor" {
		t.Fatalf("split parsing wrong: %#v", apps)
	}
	// flatpak keeps whole identifiers and skips multi-column noise.
	apps = ParseLinuxPackages("flatpak", "org.godotengine.GodotEngine\ntwo words\n")
	if len(apps) != 1 || apps[0].Identifier != "org.godotengine.GodotEngine" {
		t.Fatalf("flatpak parsing wrong: %#v", apps)
	}
}

func TestInventoryWindowsApplicationsFake(t *testing.T) {
	// This is the Windows inventory: pin the platform so the dispatcher
	// never falls through to the host's own package managers.
	pinPlatform := overridePlatform("windows")
	defer pinPlatform()
	restoreLookPath := overrideLookPath([]string{})
	defer restoreLookPath()

	// winget missing.
	if _, err := inventoryApplications(context.Background(), stubRunner(nil, nil), t.TempDir()); err == nil {
		t.Fatal("inventoried without winget")
	}
	// Export command runner fails, then a runner that writes no file.
	restoreLookPath()
	restoreLookPath = overrideLookPath([]string{"winget"})
	if _, err := inventoryApplications(context.Background(), stubRunner(nil, errors.New("winget failed")), t.TempDir()); err == nil {
		t.Fatal("accepted a failed winget export")
	}
	// Runner succeeds but writes nothing to disk.
	if _, err := inventoryApplications(context.Background(), stubRunner(nil, nil), t.TempDir()); err == nil {
		t.Fatal("accepted a missing export file")
	}
	// Malformed JSON.
	if _, err := inventoryApplications(context.Background(), fakeRunnerWriter(`{"Sources": nope`), t.TempDir()); err == nil {
		t.Fatal("accepted malformed export")
	}
	// Empty identifier.
	bad := `{"Sources":[{"Packages":[{"PackageIdentifier":""}]}]}`
	if _, err := inventoryApplications(context.Background(), fakeRunnerWriter(bad), t.TempDir()); err == nil {
		t.Fatal("accepted an empty identifier")
	}
	// Two sources, valid output.
	good := `{"Sources":[{"Packages":[{"PackageIdentifier":"Git.Git","Version":"2"},{"PackageIdentifier":"Mozilla.Firefox"}]}]}`
	apps, err := inventoryApplications(context.Background(), fakeRunnerWriter(good), t.TempDir())
	if err != nil || len(apps) != 2 || apps[0].Identifier != "Git.Git" {
		t.Fatalf("valid export failed: %v %#v", err, apps)
	}
}

func TestInstallWindowsApplicationsBranches(t *testing.T) {
	restoreLookPath := overrideLookPath([]string{"winget"})
	defer restoreLookPath()
	cleanup := overridePlatform("windows")
	defer cleanup()

	// Unsupported managers are refused wholesale.
	if err := installWindowsApplications(context.Background(), stubRunner(nil, nil), []Application{{Manager: "brew-cask", Identifier: "x"}}, filepath.Join(t.TempDir(), "i.json")); err == nil {
		t.Fatal("installed an unsupported manager")
	}
	// The empty list is a no-op.
	if err := installWindowsApplications(context.Background(), stubRunner(nil, nil), nil, filepath.Join(t.TempDir(), "i.json")); err != nil {
		t.Fatalf("empty installable list: %v", err)
	}
	// Blocked import path.
	if err := installWindowsApplications(context.Background(), stubRunner(nil, nil), []Application{{Manager: "winget", Identifier: "x"}}, filepath.Join(t.TempDir(), "missing", "i.json")); err == nil {
		t.Fatal("accepted a blocked manifest path")
	}
	// Runner failure surfaces.
	if err := installWindowsApplications(context.Background(), stubRunner(nil, errors.New("winget failed")), []Application{{Manager: "winget", Identifier: "x"}}, filepath.Join(t.TempDir(), "i.json")); err == nil {
		t.Fatal("accepted a failed winget import")
	}
	// Success.
	if err := installWindowsApplications(context.Background(), stubRunner(nil, nil), []Application{{Manager: "winget", Identifier: "x"}}, filepath.Join(t.TempDir(), "i.json")); err != nil {
		t.Fatalf("success path: %v", err)
	}
	// Non-windows destination refused.
	unclean := overridePlatform("darwin")
	if err := installWindowsApplications(context.Background(), stubRunner(nil, nil), []Application{{Manager: "winget", Identifier: "x"}}, filepath.Join(t.TempDir(), "i.json")); err == nil {
		unclean()
		t.Fatal("installed windows applications on darwin")
	}
	unclean()
}

func runInventory(t *testing.T, platform string, available []string, runner func(string, ...string) ([]byte, error)) ([]Application, error) {
	cleanup := overridePlatform(platform)
	defer cleanup()
	restore := overrideLookPath(available)
	defer restore()
	return inventoryApplications(context.Background(), fakeRunner(runner), t.TempDir())
}

func TestSystemCommandRunnerError(t *testing.T) {
	runner := systemCommandRunner{}
	if runtime.GOOS == "windows" {
		if _, err := runner.Run(context.Background(), "cmd", "/c", "exit", "1"); err == nil {
			t.Fatal("failing command accepted")
		}
	} else {
		if _, err := runner.Run(context.Background(), "sh", "-c", "exit 1"); err == nil {
			t.Fatal("failing command accepted")
		}
	}
}
