package migration

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

type recordingRunner struct {
	commands [][]string
}

func (runner *recordingRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	runner.commands = append(runner.commands, append([]string{name}, args...))
	return []byte("ok"), nil
}

func TestPortableApplicationAdaptersEndToEnd(t *testing.T) {
	brewApps, skipped := ParseBrewfile("brew \"git\"\ncask \"visual-studio-code\"\nmas \"Xcode\", id: 497799835\n")
	if len(skipped) != 0 || len(brewApps) != 3 {
		t.Fatalf("Brewfile normalization failed: apps=%#v skipped=%#v", brewApps, skipped)
	}
	brewRunner := &recordingRunner{}
	for _, application := range brewApps {
		if err := InstallApplication(context.Background(), brewRunner, application); err != nil {
			t.Fatalf("brew install planning failed for %#v: %v", application, err)
		}
	}
	if len(brewRunner.commands) != 3 || brewRunner.commands[0][0] != "brew" {
		t.Fatalf("unexpected brew commands: %#v", brewRunner.commands)
	}

	linuxApps := ParseLinuxPackages("apt", "git/stable,now 2.0 amd64 [installed]\ncurl/stable,now 8.0 amd64 [installed]\n")
	linuxRunner := &recordingRunner{}
	for _, application := range linuxApps {
		if err := InstallApplication(context.Background(), linuxRunner, application); err != nil {
			t.Fatalf("Linux install planning failed for %#v: %v", application, err)
		}
	}
	if len(linuxRunner.commands) != 2 || linuxRunner.commands[0][0] != "apt" {
		t.Fatalf("unexpected Linux commands: %#v", linuxRunner.commands)
	}

	plans := PlanApplications("windows", "darwin", []Application{
		{Manager: "winget", Identifier: "Git.Git"},
		{Manager: "winget", Identifier: "Unknown.App"},
	})
	if plans[0].Status != "installable" || plans[1].Status != "unsupported" {
		t.Fatalf("unexpected compatibility plans: %#v", plans)
	}
}

func TestRepositoryMigrationEndToEndWithResumeAndReports(t *testing.T) {
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
	writeFile(t, filepath.Join(source, "Pictures", "two.txt"), "two")

	if _, err := Capture(source, repository, key); err != nil {
		t.Fatalf("capture: %v", err)
	}
	if _, err := DryRunRestore(repository, destination, keyPath, "personal", false); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	report, err := ManagedRestore(repository, destination, keyPath, "personal", false, false)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if report.Command != "restore" || len(report.Results) != 2 {
		t.Fatalf("unexpected restore report: %#v", report)
	}
	resumed, err := ResumeRestore(repository, keyPath, "personal", false)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if len(resumed.Results) != 2 || resumed.Results[0].Status != "already-complete" {
		t.Fatalf("unexpected resume report: %#v", resumed)
	}
	results, err := VerifyDestination(repository, destination, key)
	if err != nil {
		t.Fatalf("destination verify: %v", err)
	}
	for _, result := range results {
		if result.Status != "verified" {
			t.Fatalf("unverified destination result: %#v", result)
		}
	}
}

func writeKeyForTest(path string, key []byte) error {
	return os.WriteFile(path, key, 0o600)
}
