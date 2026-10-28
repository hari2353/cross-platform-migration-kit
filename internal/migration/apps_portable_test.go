package migration

import "testing"

func TestParseBrewfileNormalizesSupportedEntries(t *testing.T) {
	applications, skipped := ParseBrewfile("# comment\nbrew \"git\"\ncask \"visual-studio-code\"\nmas \"Xcode\", id: 497799835\nurl \"https://example.com\"\n")
	if len(applications) != 3 {
		t.Fatalf("got %d applications, want 3", len(applications))
	}
	if applications[1].Manager != "brew-cask" || applications[1].Identifier != "visual-studio-code" {
		t.Fatalf("unexpected cask: %#v", applications[1])
	}
	if applications[2].Manager != "mas" {
		t.Fatalf("unexpected App Store manager: %#v", applications[2])
	}
	if len(skipped) != 1 {
		t.Fatalf("got %d skipped entries, want 1", len(skipped))
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
