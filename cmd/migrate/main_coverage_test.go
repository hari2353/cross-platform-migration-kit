package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/haris/cross-platform-migration-kit/internal/migration"
)

// This battery completes statement coverage for the CLI surface: every
// command's success path, every guard, and the report-writing fault paths,
// with fixtures only — never touching the operator's real configuration.

// ---------------------------------------------------------------------------
// Harness additions (writeFixture/silenceStdout live in main_test.go)

// breakStdout points os.Stdout at a pipe whose reader is gone, so every
// report write fails during the test.
func breakStdout(t *testing.T) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = writer
	t.Cleanup(func() {
		os.Stdout = saved
		writer.Close()
	})
}

func silenceStderr(t *testing.T) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stderr
	os.Stderr = writer
	drain := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, reader)
		close(drain)
	}()
	t.Cleanup(func() {
		os.Stderr = saved
		writer.Close()
		<-drain
		reader.Close()
	})
}

// stubHome replaces the default-source/default-destination resolver: "" and a
// non-nil error drive the failure branch, a fixture path the success branch.
func stubHome(t *testing.T, home string, homeErr error) {
	t.Helper()
	saved := userHomeDirFunc
	userHomeDirFunc = func() (string, error) { return home, homeErr }
	t.Cleanup(func() { userHomeDirFunc = saved })
}

// capturedFixture builds a repository with a plain file, a nested file, and a
// credential-bearing kubeconfig (whose scrub adds a checklist step), plus the
// matching key file.
func capturedFixture(t *testing.T) (repository, destination, keyPath string) {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "src")
	repository = filepath.Join(root, "repo")
	destination = filepath.Join(root, "dst")
	keyPath = filepath.Join(root, "migration.key")
	key := bytes.Repeat([]byte{41}, migration.KeySize)
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(source, "a.txt"), "content")
	writeFixture(t, filepath.Join(source, "nested", "inner.txt"), "deeper")
	writeFixture(t, filepath.Join(source, ".kube", "config"), "apiVersion: v1\ntoken: zzzzzzzzzzzzzzzz\n")
	if _, err := migration.Capture(source, repository, key); err != nil {
		t.Fatal(err)
	}
	return repository, destination, keyPath
}

// deleteObjectOfLastItem removes the newest item's encrypted object so a
// restore fails mid-way on every host: earlier items copy fine, the deleted
// one fails its decrypt, and the completed rows still reach the report.
func deleteObjectOfLastItem(t *testing.T, repository, keyPath string) {
	t.Helper()
	key := readTestKey(t, keyPath)
	manifest, err := migration.LoadManifest(repository, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Items) < 2 {
		t.Fatal("fixture needs at least two restore items")
	}
	last := manifest.Items[len(manifest.Items)-1]
	// "objects" is the package-internal objectDir: the CLI owns no export of
	// it, so the layout is spelled out here.
	if err := os.Remove(filepath.Join(repository, "objects", last.ID+".bin")); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// main()

func TestMainRunSuccessAndFailure(t *testing.T) {
	silenceStderr(t)
	savedExit, savedArgs := osExit, os.Args
	t.Cleanup(func() { osExit, os.Args = savedExit, savedArgs })

	// Success: a working command returns without exiting.
	root := t.TempDir()
	os.Args = []string{"migrate", "keygen", "--output", filepath.Join(root, "ok.key")}
	main()

	// Failure: the error is printed and the process exits non-zero.
	sentinel := errors.New("exit-sentinel")
	osExit = func(code int) {
		if code != 1 {
			t.Fatalf("exit code %d, want 1", code)
		}
		panic(sentinel)
	}
	os.Args = []string{"migrate"}
	func() {
		defer func() {
			if r := recover(); r != sentinel {
				t.Fatalf("main did not exit through the seam: %v", r)
			}
		}()
		main()
	}()
}

// ---------------------------------------------------------------------------
// keygen

func TestKeygenFaults(t *testing.T) {
	silenceStdout(t)
	root := t.TempDir()
	if err := run([]string{"keygen", "--bogus"}); err == nil {
		t.Fatal("keygen accepted an unknown flag")
	}
	// The output path lives under a file: the final write fails.
	blockedParent := filepath.Join(root, "blocked")
	if err := os.WriteFile(blockedParent, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"keygen", "--output", filepath.Join(blockedParent, "k.key")}); err == nil {
		t.Fatal("keygen accepted an unwritable output path")
	}
	// The generation seam surfaces its fault.
	savedGenerate := generateKey
	t.Cleanup(func() { generateKey = savedGenerate })
	generateKey = func() ([]byte, error) { return nil, errors.New("entropy exhausted") }
	if err := run([]string{"keygen", "--output", filepath.Join(root, "fault.key")}); err == nil {
		t.Fatal("keygen accepted a failing generator")
	}
}

// ---------------------------------------------------------------------------
// capture

func TestCaptureSuccessChecklistAndPortable(t *testing.T) {
	silenceStdout(t)
	repository, _, keyPath := capturedFixture(t)
	// A full CLI capture over the same source records the scrub checklist —
	// printed as finish-setup steps.
	if err := run([]string{
		"capture", "--source", filepath.Dir(repository) + string(os.PathSeparator) + "src",
		"--output", filepath.Join(repository, "again"), "--key-file", keyPath,
		"--credentials",
	}); err != nil {
		t.Fatalf("capture: %v", err)
	}
	key := readTestKey(t, keyPath)
	manifest, err := migration.LoadManifest(filepath.Join(repository, "again"), key)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Checklist) == 0 {
		t.Fatalf("scrubbed kubeconfig must yield a checklist: %#v", manifest.Checklist)
	}

	// The portable variant writes launchers plus the drive key, and copies
	// provided destination binaries.
	binaries := t.TempDir()
	for _, name := range []string{"migrate-linux-amd64", "migrate-darwin-arm64"} {
		if err := os.WriteFile(filepath.Join(binaries, name), []byte("fake-binary"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	portableRepository := filepath.Join(repository, "portable")
	if err := run([]string{
		"capture", "--source", filepath.Dir(repository) + string(os.PathSeparator) + "src",
		"--output", portableRepository, "--key-file", keyPath,
		"--portable", "--binaries", binaries,
	}); err != nil {
		t.Fatalf("portable capture: %v", err)
	}
	for _, name := range []string{"migration.key", "install.command", "install.sh", "install.bat", "migrate-linux-amd64"} {
		if _, err := os.Stat(filepath.Join(portableRepository, name)); err != nil {
			t.Fatalf("portable drive missing %s: %v", name, err)
		}
	}
}

func readTestKey(t *testing.T, keyPath string) []byte {
	t.Helper()
	key, err := migration.ReadKey(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestCaptureDefaultSourceAndFaults(t *testing.T) {
	silenceStdout(t)
	home := t.TempDir()
	// The default source resolves to the (stubbed) home fixture.
	stubHome(t, home, nil)
	if err := run([]string{
		"capture", "--output", filepath.Join(t.TempDir(), "repo"), "--key-file", writeTestKey(t),
	}); err != nil {
		t.Fatalf("default-source capture: %v", err)
	}
	// A failing home resolver is reported as such.
	stubHome(t, "", errors.New("no home"))
	if err := run([]string{
		"capture", "--output", filepath.Join(t.TempDir(), "repo"), "--key-file", writeTestKey(t),
	}); err == nil || !strings.Contains(err.Error(), "find user home") {
		t.Fatalf("home fault not surfaced: %v", err)
	}
	// An unreadable key fails before capture.
	stubHome(t, home, nil)
	if err := run([]string{
		"capture", "--output", filepath.Join(t.TempDir(), "repo"), "--key-file", filepath.Join(t.TempDir(), "missing.key"),
	}); err == nil {
		t.Fatal("capture accepted a missing key file")
	}
	// Flag parse errors surface directly.
	if err := run([]string{"capture", "--bogus"}); err == nil {
		t.Fatal("capture accepted an unknown flag")
	}
	// A missing source root fails the inventory.
	if err := run([]string{
		"capture", "--source", filepath.Join(t.TempDir(), "vanishing"), "--output", filepath.Join(t.TempDir(), "repo"),
		"--key-file", writeTestKey(t),
	}); err == nil {
		t.Fatal("capture accepted a missing source root")
	}
}

func writeTestKey(t *testing.T) string {
	t.Helper()
	keyPath := filepath.Join(t.TempDir(), "k.key")
	if err := os.WriteFile(keyPath, bytes.Repeat([]byte{42}, migration.KeySize), 0o600); err != nil {
		t.Fatal(err)
	}
	return keyPath
}

// ---------------------------------------------------------------------------
// inspect

func TestInspectSuccessAndFaults(t *testing.T) {
	silenceStdout(t)
	repository, _, keyPath := capturedFixture(t)
	if err := run([]string{"inspect", "--bundle", repository, "--key-file", keyPath}); err != nil {
		t.Fatalf("inspect: %v", err)
	}
	// The wrong key (or a corrupted bundle) fails the read.
	wrongKey := filepath.Join(t.TempDir(), "wrong.key")
	if err := os.WriteFile(wrongKey, bytes.Repeat([]byte{43}, migration.KeySize), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"inspect", "--bundle", repository, "--key-file", wrongKey}); err == nil {
		t.Fatal("inspect accepted the wrong key")
	}
	if err := run([]string{"inspect", "--bogus"}); err == nil {
		t.Fatal("inspect accepted an unknown flag")
	}
	if err := run([]string{"inspect", "--bundle", repository}); err == nil {
		t.Fatal("inspect accepted a missing --key-file")
	}
}

// ---------------------------------------------------------------------------
// restore

func TestRestoreDryRunSuccessAndReportFailure(t *testing.T) {
	silenceStdout(t)
	repository, destination, keyPath := capturedFixture(t)
	// Dry-run prints the plan.
	if err := run([]string{
		"restore", "--bundle", repository, "--destination", destination, "--key-file", keyPath, "--dry-run",
	}); err != nil {
		t.Fatalf("dry-run restore: %v", err)
	}
	// Default destination comes from the (stubbed) home resolver; a separate
	// repository keeps this dry-run's state apart from the one above.
	stubHome(t, t.TempDir(), nil)
	defaultRepository, _, defaultKeyPath := capturedFixture(t)
	if err := run([]string{
		"restore", "--bundle", defaultRepository, "--key-file", defaultKeyPath, "--dry-run", "--overwrite",
	}); err != nil {
		t.Fatalf("default-destination dry run: %v", err)
	}
	stubHome(t, "", errors.New("no home"))
	if err := run([]string{
		"restore", "--bundle", defaultRepository, "--key-file", defaultKeyPath, "--dry-run",
	}); err == nil || !strings.Contains(err.Error(), "find user home") {
		t.Fatalf("home fault not surfaced: %v", err)
	}
	stubHome(t, t.TempDir(), nil)

	// Flag parse errors surface directly.
	if err := run([]string{"restore", "--bogus"}); err == nil {
		t.Fatal("restore accepted an unknown flag")
	}

	// Successful restore prints the report. The dry-run state was left by
	// the first command, and nothing moved since.
	if err := run([]string{
		"restore", "--bundle", repository, "--destination", destination, "--key-file", keyPath,
	}); err != nil {
		t.Fatalf("restore: %v", err)
	}

	// A restore that fails mid-way still prints its completed rows and
	// wraps the error: a fresh fixture whose newest object is deleted makes
	// the first file copy and the last file fail its decrypt, on every host
	// without depending on platform permission models.
	freshRepository, freshDestination, freshKeyPath := capturedFixture(t)
	if _, err := migration.DryRunRestore(freshRepository, freshDestination, freshKeyPath, "personal", false); err != nil {
		t.Fatal(err)
	}
	deleteObjectOfLastItem(t, freshRepository, freshKeyPath)
	err := run([]string{
		"restore", "--bundle", freshRepository, "--destination", freshDestination, "--key-file", freshKeyPath,
	})
	if err == nil || !strings.Contains(err.Error(), "restore failed:") {
		t.Fatalf("restore failure report missing: %v", err)
	}
	// A plain failure without completed rows prints no report.
	if err := run([]string{
		"restore", "--bundle", filepath.Join(t.TempDir(), "missing"), "--destination", filepath.Join(t.TempDir(), "dst"),
		"--key-file", keyPath,
	}); err == nil || strings.Contains(err.Error(), "restore failed:") {
		t.Fatalf("bare failure handling wrong: %v", err)
	}
}

// ---------------------------------------------------------------------------
// verify

func TestVerifyRepositoryOnlyAndFaults(t *testing.T) {
	silenceStdout(t)
	repository, destination, keyPath := capturedFixture(t)
	if _, err := migration.DryRunRestore(repository, destination, keyPath, "personal", false); err != nil {
		t.Fatal(err)
	}
	if _, err := migration.ManagedRestore(repository, destination, keyPath, "personal", false, false); err != nil {
		t.Fatal(err)
	}
	// Repository-only verification passes.
	if err := run([]string{"verify", "--bundle", repository, "--key-file", keyPath}); err != nil {
		t.Fatalf("repository verify: %v", err)
	}
	// Flag parse errors surface directly.
	if err := run([]string{"verify", "--bogus"}); err == nil {
		t.Fatal("verify accepted an unknown flag")
	}
	// The key must exist and be exactly key-sized.
	if err := run([]string{"verify", "--bundle", repository, "--key-file", filepath.Join(t.TempDir(), "missing.key")}); err == nil {
		t.Fatal("verify accepted a missing key")
	}
	// The repository must be verifiable under that key.
	wrongKey := filepath.Join(t.TempDir(), "wrong.key")
	if err := os.WriteFile(wrongKey, bytes.Repeat([]byte{44}, migration.KeySize), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"verify", "--bundle", repository, "--key-file", wrongKey}); err == nil {
		t.Fatal("verify accepted the wrong key")
	}
	// A destination verify whose report write fails returns the writer
	// error rather than the verification verdict.
	breakStdout(t)
	if err := run([]string{
		"verify", "--bundle", repository, "--key-file", keyPath, "--destination", destination,
	}); err == nil {
		t.Fatal("writer fault not surfaced")
	}
	// Missing destination rows also fail.
	if err := run([]string{
		"verify", "--bundle", repository, "--key-file", keyPath, "--destination", filepath.Join(t.TempDir(), "empty"),
	}); err == nil {
		t.Fatal("verify accepted an empty destination")
	}
}

// ---------------------------------------------------------------------------
// resume

func TestResumeSuccessAndFaults(t *testing.T) {
	silenceStdout(t)
	repository, destination, keyPath := capturedFixture(t)
	// Resume without a completed dry-run fails.
	if err := run([]string{"resume", "--bundle", repository, "--key-file", keyPath}); err == nil {
		t.Fatal("resume accepted a missing restore state")
	}
	// A completed dry-run plus restore makes resume report the finished rows.
	if _, err := migration.DryRunRestore(repository, destination, keyPath, "personal", false); err != nil {
		t.Fatal(err)
	}
	if _, err := migration.ManagedRestore(repository, destination, keyPath, "personal", false, false); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"resume", "--bundle", repository, "--key-file", keyPath}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if err := run([]string{"resume", "--bogus"}); err == nil {
		t.Fatal("resume accepted an unknown flag")
	}
}

// ---------------------------------------------------------------------------
// pack

func TestPackSuccessAndFaults(t *testing.T) {
	silenceStdout(t)
	repository, _, keyPath := capturedFixture(t)
	if err := run([]string{"pack"}); err == nil {
		t.Fatal("pack accepted missing arguments")
	}
	if err := run([]string{"pack", "--bundle", repository}); err == nil {
		t.Fatal("pack accepted a missing --key-file")
	}
	// A repository without a manifest is refused.
	if err := run([]string{"pack", "--bundle", t.TempDir(), "--key-file", keyPath}); err == nil {
		t.Fatal("pack accepted an unpacked directory")
	}
	if err := run([]string{"pack", "--bundle", repository, "--key-file", keyPath}); err != nil {
		t.Fatalf("pack: %v", err)
	}
}

// ---------------------------------------------------------------------------
// doctor

func TestDoctorAdvisoryDestinationAndWriterFault(t *testing.T) {
	silenceStdout(t)
	repository, destination, keyPath := capturedFixture(t)
	// Advisory destination findings never block the doctor.
	if err := run([]string{
		"doctor", "--bundle", repository, "--key-file", keyPath, "--destination", destination,
	}); err != nil {
		t.Fatalf("doctor: %v", err)
	}
	// The report write failing surfaces the writer error.
	breakStdout(t)
	if err := run([]string{"doctor", "--bundle", repository, "--key-file", keyPath}); err == nil {
		t.Fatal("writer fault not surfaced")
	}
	if err := run([]string{"doctor", "--bogus"}); err == nil {
		t.Fatal("doctor accepted an unknown flag")
	}
}

// ---------------------------------------------------------------------------
// Application intents through reviewed shims (never a real package manager).
// A Windows host finds the winget shim; a POSIX host finds the apk shim —
// the fixture PATH carries both, and each platform probes only its own.

func applicationShimPath(t *testing.T) string {
	t.Helper()
	shim := t.TempDir()
	payload := `{"Sources":[{"Packages":[{"PackageIdentifier":"Git.Git","Version":"2.40.0"}]}]}`
	// winget shim for Windows hosts, writing the export document the
	// capture reads back into its --output path argument.
	script := "@echo off\r\n" +
		"echo " + payload + " > \"%~3\"\r\n" +
		"exit /b 0\r\n"
	if err := os.WriteFile(filepath.Join(shim, "winget.cmd"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// apk shim for POSIX hosts: a plain package list the Linux inventory
	// parses line by line.
	if err := os.WriteFile(filepath.Join(shim, "apk"), []byte("#!/bin/sh\necho shim-package\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return shim
}

func TestCaptureApplicationsThroughShimAndInspect(t *testing.T) {
	silenceStdout(t)
	// The only PATH entries are the reviewed shims: the capture runs its
	// usual package-manager export, which executes the shim — never a real
	// package manager — and the manifest records the package intent.
	t.Setenv("PATH", applicationShimPath(t))
	source := t.TempDir()
	repository := filepath.Join(t.TempDir(), "apps-repo")
	keyPath := writeTestKey(t)
	if err := run([]string{
		"capture", "--source", source, "--output", repository, "--key-file", keyPath, "--apps",
	}); err != nil {
		t.Fatalf("shimmed application capture: %v", err)
	}
	key := readTestKey(t, keyPath)
	manifest, err := migration.LoadManifest(repository, key)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		if len(manifest.Applications) != 1 || manifest.Applications[0].Identifier != "Git.Git" || manifest.Applications[0].Version != "2.40.0" {
			t.Fatalf("shimmed application intent missing: %#v", manifest.Applications)
		}
	} else {
		if len(manifest.Applications) != 1 || manifest.Applications[0].Identifier != "shim-package" {
			t.Fatalf("shimmed package intent missing: %#v", manifest.Applications)
		}
	}
	// inspect prints the application intents.
	if err := run([]string{"inspect", "--bundle", repository, "--key-file", keyPath}); err != nil {
		t.Fatalf("inspect with applications: %v", err)
	}
	if err := run([]string{"inspect", "--bundle", repository, "--key-file", keyPath, "--profile", "unknown"}); err == nil {
		t.Fatal("inspect accepted an unknown profile")
	}
}

// ---------------------------------------------------------------------------
// Remaining restore/verify/pack guards

func TestRestoreAndVerifyRemainingGuards(t *testing.T) {
	silenceStdout(t)
	// A dry-run over a missing repository fails inside the CLI.
	if err := run([]string{
		"restore", "--bundle", filepath.Join(t.TempDir(), "missing"), "--destination", filepath.Join(t.TempDir(), "dst"),
		"--key-file", writeTestKey(t), "--dry-run",
	}); err == nil {
		t.Fatal("dry-run accepted a missing repository")
	}

	// A failing mid-restore report write surfaces the writer error, not the
	// restore error. The deleted object makes the failure host-independent.
	reportRepository, reportDestination, reportKeyPath := capturedFixture(t)
	if _, err := migration.DryRunRestore(reportRepository, reportDestination, reportKeyPath, "personal", false); err != nil {
		t.Fatal(err)
	}
	deleteObjectOfLastItem(t, reportRepository, reportKeyPath)
	breakStdout(t)
	if err := run([]string{
		"restore", "--bundle", reportRepository, "--destination", reportDestination, "--key-file", reportKeyPath,
	}); err == nil || strings.Contains(err.Error(), "restore failed:") {
		t.Fatalf("report writer fault not surfaced: %v", err)
	}

	// verify --destination reads the manifest itself: a corrupted bundle
	// fails before any row prints.
	corruptRepository, corruptDestination, corruptKeyPath := capturedFixture(t)
	if err := os.WriteFile(filepath.Join(corruptRepository, "manifest.enc"), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{
		"verify", "--bundle", corruptRepository, "--key-file", corruptKeyPath, "--destination", corruptDestination,
	}); err == nil {
		t.Fatal("verify accepted a corrupted bundle")
	}

	// pack rejects flag parse errors.
	if err := run([]string{"pack", "--bogus"}); err == nil {
		t.Fatal("pack accepted an unknown flag")
	}
}
