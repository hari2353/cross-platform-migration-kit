package migration

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestParseBrewfileNormalizesSupportedEntries(t *testing.T) {
	applications, skipped := ParseBrewfile("tap \"homebrew/bundle\"\n# comment\nbrew \"git\"\ncask \"visual-studio-code\"\nmas \"Xcode\", id: 497799835\nurl \"https://example.com\"\n")
	if len(applications) != 3 {
		t.Fatalf("got %d applications, want 3", len(applications))
	}
	if applications[0].Manager != "brew-brew" || applications[0].Identifier != "git" {
		t.Fatalf("unexpected first entry: %#v", applications[0])
	}
	if applications[2].Manager != "mas" {
		t.Fatalf("unexpected App Store manager: %#v", applications[2])
	}
	if len(skipped) != 1 {
		t.Fatalf("got %d skipped entries, want 1: %#v", len(skipped), skipped)
	}
}

func TestLinuxInstallCommandUsesArgumentArray(t *testing.T) {
	command, ok := LinuxInstallCommand(Application{Manager: "apt", Identifier: "git"})
	if !ok || command.Manager != "apt" || len(command.Args) != 3 || command.Args[2] != "git" {
		t.Fatalf("unexpected apt command: %#v", command)
	}
	if _, ok := LinuxInstallCommand(Application{Manager: "unknown", Identifier: "git"}); ok {
		t.Fatal("unknown manager was accepted")
	}
}

func TestWindowsToMacApplicationMappingIsCurated(t *testing.T) {
	plans := PlanApplications("windows", "darwin", []Application{
		{Manager: "winget", Identifier: "Git.Git"},
		{Manager: "winget", Identifier: "Unknown.App"},
	})
	if plans[0].Status != "installable" || plans[0].Target.Identifier != "git" {
		t.Fatalf("unexpected mapped plan: %#v", plans[0])
	}
	if plans[1].Status != "unsupported" {
		t.Fatalf("unknown app was treated as installable: %#v", plans[1])
	}
}

func TestParseLinuxPackagesDeduplicatesIdentifiers(t *testing.T) {
	applications := ParseLinuxPackages("apt", "Listing...\ngit/stable,now 2.0 amd64 [installed]\ngit/stable,now 2.0 amd64 [installed]\ncurl/stable,now 8.0 amd64 [installed]\n")
	if len(applications) != 2 || applications[0].Identifier != "git" {
		t.Fatalf("unexpected packages: %#v", applications)
	}
}

func TestWindowsToLinuxApplicationMappingIsCurated(t *testing.T) {
	original := platformLookPath
	t.Cleanup(func() { platformLookPath = original })
	platformLookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }

	plans := PlanApplications("windows", "linux", []Application{
		{Manager: "winget", Identifier: "Git.Git"},
		{Manager: "winget", Identifier: "Google.Chrome"},
		{Manager: "winget", Identifier: "Unknown.App"},
	})
	if plans[0].Status != "installable" || plans[0].Target.Manager != "linux" || plans[0].Target.Identifier != "git" {
		t.Fatalf("unexpected Linux git plan: %#v", plans[0])
	}
	if plans[1].Status != "installable" || plans[1].Target.Manager != "flatpak" || plans[1].Target.Identifier != "com.google.Chrome" {
		t.Fatalf("unexpected Linux chrome plan: %#v", plans[1])
	}
	if plans[2].Status != "unsupported" {
		t.Fatalf("unknown app was treated as installable: %#v", plans[2])
	}
}

func TestMacToWindowsApplicationMappingIsCurated(t *testing.T) {
	plans := PlanApplications("darwin", "windows", []Application{
		{Manager: "brew-cask", Identifier: "google-chrome"},
		{Manager: "brew-brew", Identifier: "git"},
		{Manager: "mas", Identifier: "497799835"},
	})
	if plans[0].Status != "installable" || plans[0].Target.Manager != "winget" || plans[0].Target.Identifier != "Google.Chrome" {
		t.Fatalf("unexpected winget chrome plan: %#v", plans[0])
	}
	if plans[1].Status != "installable" || plans[1].Target.Identifier != "Git.Git" {
		t.Fatalf("unexpected winget git plan: %#v", plans[1])
	}
	if plans[2].Status != "unsupported" {
		t.Fatalf("App Store intent was mapped to a Windows plan: %#v", plans[2])
	}
}

func TestLinuxToMacApplicationMappingIsCurated(t *testing.T) {
	plans := PlanApplications("linux", "darwin", []Application{
		{Manager: "apt", Identifier: "git"},
		{Manager: "dnf", Identifier: "firefox"},
		{Manager: "snap", Identifier: "vlc"},
	})
	if plans[0].Status != "installable" || plans[0].Target.Manager != "brew-brew" {
		t.Fatalf("unexpected brew git plan: %#v", plans[0])
	}
	if plans[1].Status != "installable" || plans[1].Target.Manager != "brew-cask" {
		t.Fatalf("unexpected brew firefox plan: %#v", plans[1])
	}
	if plans[2].Status != "installable" || plans[2].Target.Identifier != "vlc" {
		t.Fatalf("unexpected snap vlc plan: %#v", plans[2])
	}
}

func TestLinuxDestinationPlanRequiresPackageManager(t *testing.T) {
	original := platformLookPath
	t.Cleanup(func() { platformLookPath = original })
	platformLookPath = func(name string) (string, error) { return "", os.ErrNotExist }

	plans := PlanApplications("windows", "linux", []Application{
		{Manager: "winget", Identifier: "Git.Git"},
		{Manager: "winget", Identifier: "Google.Chrome"},
	})
	if plans[0].Status != "unsupported" || plans[1].Status != "unsupported" {
		t.Fatalf("plans ignored destination capability: %#v", plans)
	}
}

func TestApplicationCommandResolvesLinuxManager(t *testing.T) {
	original := platformLookPath
	t.Cleanup(func() { platformLookPath = original })
	platformLookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }

	command, ok := applicationCommand(Application{Manager: "linux", Identifier: "git"})
	if !ok || command.Manager != "apt" || len(command.Args) != 3 || command.Args[2] != "git" {
		t.Fatalf("unexpected resolved command: %#v %v", command, ok)
	}

	platformLookPath = func(name string) (string, error) { return "", os.ErrNotExist }
	if _, ok := applicationCommand(Application{Manager: "linux", Identifier: "git"}); ok {
		t.Fatal("linux command was planned without any package manager")
	}
}

type stubbedLookPathRunner struct {
	commands [][]string
	outputs  map[string]string
	failures map[string]bool
}

func (runner *stubbedLookPathRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	runner.commands = append(runner.commands, append([]string{name}, args...))
	if runner.failures[name] {
		return nil, errors.New(name + " failed")
	}
	return []byte(runner.outputs[name]), nil
}

func TestLinuxInventoryMergesAllDetectedManagers(t *testing.T) {
	original := platformLookPath
	t.Cleanup(func() { platformLookPath = original })
	platformLookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }

	runner := &stubbedLookPathRunner{
		outputs: map[string]string{
			"flatpak": "org.mozilla.firefox\norg.videolan.VLC\n",
			"snap":    "",
			"apt":     "git/stable,now 2.0 amd64 [installed]\ncurl/stable,now 8.0 amd64 [installed]\n",
		},
	}
	applications, err := inventoryLinuxApplications(context.Background(), runner)
	if err != nil {
		t.Fatalf("inventory linux applications: %v", err)
	}
	managers := map[string]bool{}
	for _, application := range applications {
		managers[application.Manager] = true
	}
	if !managers["flatpak"] || !managers["apt"] {
		t.Fatalf("multi-manager inventory lost packages: %#v", applications)
	}
}

func TestLinuxInventoryFailsWithoutManagers(t *testing.T) {
	original := platformLookPath
	t.Cleanup(func() { platformLookPath = original })
	platformLookPath = func(name string) (string, error) { return "", os.ErrNotExist }

	if _, err := inventoryLinuxApplications(context.Background(), &stubbedLookPathRunner{}); err == nil {
		t.Fatal("inventory succeeded without any package manager")
	}
}
