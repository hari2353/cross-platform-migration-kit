package migration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"testing"
)

func TestParseWingetExport(t *testing.T) {
	applications, err := parseWingetExport([]byte(`{
  "Sources": [{
    "Packages": [
      {"PackageIdentifier": "Microsoft.VisualStudioCode", "Version": "1.2.3"},
      {"PackageIdentifier": "Git.Git"}
    ]
  }]
}`))
	if err != nil {
		t.Fatalf("parse export: %v", err)
	}
	if len(applications) != 2 {
		t.Fatalf("got %d applications, want 2", len(applications))
	}
	if applications[0].Manager != "winget" || applications[0].Identifier != "Microsoft.VisualStudioCode" {
		t.Fatalf("unexpected first application: %#v", applications[0])
	}
}

func TestParseWingetExportRejectsMissingIdentifier(t *testing.T) {
	if _, err := parseWingetExport([]byte(`{"Sources":[{"Packages":[{"Version":"1.2.3"}]}]}`)); err == nil {
		t.Fatal("package without identifier was accepted")
	}
}

func TestPlanApplicationInstallRejectsUnknownManager(t *testing.T) {
	installable, skipped := planApplicationInstall([]Application{
		{Manager: "winget", Identifier: "Git.Git"},
		{Manager: "unknown", Identifier: "example"},
	})
	if len(installable) != 1 || len(skipped) != 1 || skipped[0] != "example" {
		t.Fatalf("unexpected plan: installable=%#v skipped=%#v", installable, skipped)
	}
}

type fakeWingetRunner struct {
	commands [][]string
}

func (runner *fakeWingetRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	runner.commands = append(runner.commands, append([]string{name}, args...))
	if len(args) > 0 && args[0] == "export" {
		for index, argument := range args {
			if argument == "--output" && index+1 < len(args) {
				return nil, os.WriteFile(args[index+1], []byte(`{"Packages":[{"PackageIdentifier":"Git.Git","Version":"2.0"}]}`), 0o600)
			}
		}
	}
	return []byte("ok"), nil
}

func TestWindowsApplicationInstallUsesValidatedWingetArguments(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("winget adapter is Windows-specific")
	}
	runner := &fakeWingetRunner{}
	manifestPath := t.TempDir() + "\\winget-import.json"
	if err := installWindowsApplications(context.Background(), runner, []Application{{Manager: "winget", Identifier: "Git.Git", Version: "2.0"}}, manifestPath); err != nil {
		t.Fatalf("install applications: %v", err)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("got %d commands, want one", len(runner.commands))
	}
	command := runner.commands[0]
	if command[0] != "winget" || command[1] != "import" || command[2] != "--import-file" {
		t.Fatalf("unexpected command: %#v", command)
	}
	if _, err := os.Stat(manifestPath); !os.IsNotExist(err) {
		t.Fatalf("temporary import manifest was not removed: %v", err)
	}
}

func TestApplicationManifestShapeMatchesWingetImportContract(t *testing.T) {
	contents, err := json.Marshal(wingetManifest{Sources: []wingetSource{{Packages: []wingetPackage{{Identifier: "Git.Git", Version: "2.0"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != `{"Sources":[{"Packages":[{"PackageIdentifier":"Git.Git","Version":"2.0"}]}]}` {
		t.Fatalf("unexpected manifest: %s", contents)
	}
}

type brewfileDumpRunner struct {
	commands [][]string
	path     string
	contents string
}

func (runner *brewfileDumpRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	runner.commands = append(runner.commands, append([]string{name}, args...))
	for index, argument := range args {
		if argument == "--file" && index+1 < len(args) {
			runner.path = args[index+1]
		}
	}
	if err := os.WriteFile(runner.path, []byte(runner.contents), 0o600); err != nil {
		return nil, err
	}
	return []byte("ok"), nil
}

func TestInventoryDarwinKeepsIntentsWhenBrewfileHasUnknownLines(t *testing.T) {
	original := platformLookPath
	t.Cleanup(func() { platformLookPath = original })
	platformLookPath = func(name string) (string, error) { return "/usr/local/bin/" + name, nil }

	runner := &brewfileDumpRunner{contents: "brew \"git\"\ntap \"homebrew/cask\"\ncask \"visual-studio-code\"\nsomething-unknown\n"}
	applications, err := inventoryDarwinApplications(context.Background(), runner, t.TempDir())
	if err == nil {
		t.Fatal("expected a warning error for the unsupported line")
	}
	if len(applications) != 2 {
		t.Fatalf("partial inventory was dropped: %#v", applications)
	}
	if applications[0].Identifier != "git" || applications[1].Identifier != "visual-studio-code" {
		t.Fatalf("unexpected applications: %#v", applications)
	}
}

func TestInventoryDarwinFailsWithoutBrew(t *testing.T) {
	original := platformLookPath
	t.Cleanup(func() { platformLookPath = original })
	platformLookPath = func(name string) (string, error) { return "", os.ErrNotExist }

	if _, err := inventoryDarwinApplications(context.Background(), nil, t.TempDir()); err == nil {
		t.Fatal("inventory succeeded without brew")
	}
}

// pendingExportRunner writes a winget export file and then fails, simulating a
// package manager that produced data but exited non-zero.
type pendingExportRunner struct {
	commands [][]string
}

func (runner *pendingExportRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	runner.commands = append(runner.commands, append([]string{name}, args...))
	for index, argument := range args {
		if argument == "--output" && index+1 < len(args) {
			_ = os.WriteFile(args[index+1], []byte(`{"Sources":[{"Packages":[{"PackageIdentifier":"Git.Git"}]}]}`), 0o600)
		}
	}
	return []byte("winget complained"), errors.New("winget exited non-zero")
}

func TestInventoryWindowsReportsFailuresWithParsedPackages(t *testing.T) {
	original := platformLookPath
	t.Cleanup(func() { platformLookPath = original })
	platformLookPath = func(name string) (string, error) { return "C:\\Windows\\" + name, nil }

	tempDir := t.TempDir()
	applications, err := inventoryWindowsApplications(context.Background(), &pendingExportRunner{}, tempDir)
	if err == nil {
		t.Fatal("expected the winget failure to be reported")
	}
	if len(applications) != 0 {
		t.Fatalf("winget export failure must not invent packages: %#v", applications)
	}
}
