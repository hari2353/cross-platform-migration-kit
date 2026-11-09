package migration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Application struct {
	Manager    string `json:"manager"`
	Identifier string `json:"identifier"`
	Version    string `json:"version,omitempty"`
}

type ApplicationCommand struct {
	Manager string
	Args    []string
}

type ApplicationPlan struct {
	Source Application
	Target Application
	Status string
	Reason string
}

func PlanApplications(sourceOS, destinationOS string, applications []Application) []ApplicationPlan {
	plans := make([]ApplicationPlan, 0, len(applications))
	for _, application := range applications {
		plan := ApplicationPlan{Source: application, Status: "unsupported", Reason: "no destination adapter"}
		target, ok := equivalentApplication(sourceOS, destinationOS, application)
		if ok {
			plan.Target = target
			plan.Status = "installable"
			plan.Reason = "reviewed equivalent"
		} else if sourceOS == destinationOS && application.Manager != "" {
			plan.Target = application
			plan.Status = "native"
			plan.Reason = "same-platform package intent"
		}
		plans = append(plans, plan)
	}
	return plans
}

func equivalentApplication(sourceOS, destinationOS string, application Application) (Application, bool) {
	if sourceOS == destinationOS {
		return application, true
	}
	key := strings.ToLower(application.Identifier)
	mappings := map[string]Application{
		"git.git":                    {Manager: "brew-brew", Identifier: "git"},
		"openjs.nodejs.lts":          {Manager: "brew-brew", Identifier: "node"},
		"microsoft.visualstudiocode": {Manager: "brew-cask", Identifier: "visual-studio-code"},
		"google.chrome":              {Manager: "brew-cask", Identifier: "google-chrome"},
		"brave.brave":                {Manager: "brew-cask", Identifier: "brave-browser"},
		"mozilla.firefox":            {Manager: "brew-cask", Identifier: "firefox"},
		"videolan.vlc":               {Manager: "brew-cask", Identifier: "vlc"},
	}
	if target, ok := mappings[key]; ok {
		if destinationOS == "darwin" {
			return target, true
		}
	}
	return Application{}, false
}

func ParseBrewfile(contents string) ([]Application, []string) {
	var applications []Application
	var skipped []string
	for _, line := range strings.Split(contents, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		// Tap lines are re-derivable on the destination; they are never an
		// error and never an application intent.
		if fields[0] == "tap" {
			continue
		}
		if len(fields) >= 2 && (fields[0] == "brew" || fields[0] == "cask" || fields[0] == "mas") {
			identifier := strings.Trim(fields[1], "\"")
			if identifier == "" {
				skipped = append(skipped, line)
				continue
			}
			manager := "brew-" + fields[0]
			if fields[0] == "mas" {
				manager = "mas"
			}
			applications = append(applications, Application{Manager: manager, Identifier: identifier})
			continue
		}
		skipped = append(skipped, line)
	}
	return applications, skipped
}

// linuxInstallArgs builds the silent-install argument vector for a native
// Linux package manager; apt/dnf differ in flag spelling by ecosystem.
func linuxInstallArgs(manager, identifier string) []string {
	switch manager {
	case "apk":
		return []string{"add", "--no-interactive", identifier}
	case "pacman":
		return []string{"install", "--noconfirm", identifier}
	case "zypper":
		return []string{"install", "--non-interactive", identifier}
	default:
		return []string{"install", "-y", identifier}
	}
}

func LinuxInstallCommand(application Application) (ApplicationCommand, bool) {
	if application.Identifier == "" {
		return ApplicationCommand{}, false
	}
	switch application.Manager {
	case "apt", "dnf", "pacman", "zypper", "apk", "flatpak", "snap":
		args := linuxInstallArgs(application.Manager, application.Identifier)
		if application.Manager == "flatpak" {
			args = []string{"install", "-y", application.Identifier}
		} else if application.Manager == "snap" {
			args = []string{"install", application.Identifier}
		}
		return ApplicationCommand{Manager: application.Manager, Args: args}, true
	default:
		return ApplicationCommand{}, false
	}
}

func InstallApplication(ctx context.Context, runner CommandRunner, application Application) error {
	command, ok := applicationCommand(application)
	if !ok {
		return fmt.Errorf("unsupported application manager %q", application.Manager)
	}
	_, err := runner.Run(ctx, command.Manager, command.Args...)
	return err
}

// nativeLinuxManagers is the preference order used when a bundle carries the
// generic "linux" manager and the destination must pick its own tooling.
var nativeLinuxManagers = []string{"apt", "dnf", "pacman", "zypper", "apk", "flatpak", "snap"}

func applicationCommand(application Application) (ApplicationCommand, bool) {
	switch application.Manager {
	case "brew-brew":
		return ApplicationCommand{Manager: "brew", Args: []string{"install", application.Identifier}}, true
	case "brew-cask":
		return ApplicationCommand{Manager: "brew", Args: []string{"install", "--cask", application.Identifier}}, true
	case "mas":
		return ApplicationCommand{Manager: "mas", Args: []string{"install", application.Identifier}}, true
	case "winget":
		return ApplicationCommand{Manager: "winget", Args: []string{"install", "--id", application.Identifier, "--exact"}}, true
	case "linux":
		// The generic Linux manager defers to whatever native manager is
		// actually installed on the destination.
		for _, manager := range nativeLinuxManagers {
			if _, err := platformLookPath(manager); err == nil {
				if _, ok := LinuxInstallCommand(Application{Manager: manager, Identifier: application.Identifier}); ok {
					return ApplicationCommand{Manager: manager, Args: linuxInstallArgs(manager, application.Identifier)}, true
				}
			}
		}
		return ApplicationCommand{}, false
	default:
		return LinuxInstallCommand(application)
	}
}

func ParseLinuxPackages(manager string, contents string) []Application {
	applications := []Application{}
	seen := map[string]bool{}
	for _, line := range strings.Split(contents, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if manager == "flatpak" {
			// flatpak keeps whole reverse-DNS identifiers but emits
			// multi-column noise lines that must be skipped.
			if len(fields) > 1 {
				continue
			}
			if !seen[fields[0]] {
				applications = append(applications, Application{Manager: manager, Identifier: fields[0]})
				seen[fields[0]] = true
			}
			continue
		}
		identifier := fields[0]
		if strings.Contains(identifier, "/") {
			identifier = strings.SplitN(identifier, "/", 2)[0]
		}
		if identifier == "Listing..." {
			continue
		}
		if !seen[identifier] {
			applications = append(applications, Application{Manager: manager, Identifier: identifier})
			seen[identifier] = true
		}
	}
	return applications
}

type wingetPackage struct {
	Identifier string `json:"PackageIdentifier"`
	Version    string `json:"Version,omitempty"`
}

type wingetSource struct {
	Packages []wingetPackage `json:"Packages"`
}

type wingetManifest struct {
	Sources []wingetSource `json:"Sources"`
}

type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type systemCommandRunner struct{}

func (systemCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%s failed: %w: %s", name, err, output)
	}
	return output, nil
}

// inventoryApplications probes the platform's own tooling first and falls
// back to the Linux manager list anywhere else: a shimmed apk/pacman tool is
// still a faithful inventory source when the native tool is unavailable.
// shortcut: on an unsupported platform the fallback probes real PATH, so a
// Linux CI host could run a real manager there — fine on this macOS host.
func inventoryApplications(ctx context.Context, runner CommandRunner, tempDir string) ([]Application, error) {
	if runner == nil {
		runner = systemCommandRunner{}
	}
	if goosFunc() == "windows" {
		if _, err := platformLookPath("winget"); err == nil {
			return inventoryWindowsApplications(ctx, runner, tempDir)
		}
	}
	if goosFunc() == "darwin" {
		if _, err := platformLookPath("brew"); err == nil {
			return inventoryDarwinApplications(ctx, runner, tempDir)
		}
	}
	for _, manager := range nativeLinuxManagers {
		if _, err := platformLookPath(manager); err == nil {
			return inventoryLinuxApplications(ctx, runner)
		}
	}
	return nil, errors.New("no supported application inventory tool is available")
}

// inventoryWindowsApplications reads the winget export the runner produced.
func inventoryWindowsApplications(ctx context.Context, runner CommandRunner, tempDir string) ([]Application, error) {
	if runner == nil {
		runner = systemCommandRunner{}
	}
	if _, err := platformLookPath("winget"); err != nil {
		return nil, fmt.Errorf("winget is unavailable: %w", err)
	}
	outputPath := filepath.Join(tempDir, "winget-export.json")
	defer os.Remove(outputPath)
	if _, err := runner.Run(ctx, "winget", "export", "--output", outputPath, "--accept-source-agreements", "--disable-interactivity"); err != nil {
		return nil, err
	}
	contents, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("read winget export: %w", err)
	}
	return parseWingetExport(contents)
}

// inventoryDarwinApplications dumps the Homebrew bundle list into the
// inventory workspace and parses it; Brewfile entries skip unsupported lines
// without discarding the rest.
func inventoryDarwinApplications(ctx context.Context, runner CommandRunner, tempDir string) ([]Application, error) {
	if runner == nil {
		runner = systemCommandRunner{}
	}
	if _, err := platformLookPath("brew"); err != nil {
		return nil, fmt.Errorf("brew is unavailable: %w", err)
	}
	outputPath := filepath.Join(tempDir, "Brewfile")
	if _, err := runner.Run(ctx, "brew", "bundle", "dump", "--file", outputPath, "--force"); err != nil {
		return nil, err
	}
	contents, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, err
	}
	applications, skipped := ParseBrewfile(string(contents))
	if len(skipped) > 0 {
		return applications, fmt.Errorf("Brewfile contains unsupported entries: %d", len(skipped))
	}
	return applications, nil
}

// inventoryLinuxApplications walks the native manager list in preference
// order; a manager that answers with an empty listing is skipped like a
// failing one so the next candidate still gets a chance.
func inventoryLinuxApplications(ctx context.Context, runner CommandRunner) ([]Application, error) {
	if runner == nil {
		runner = systemCommandRunner{}
	}
	managers := []struct {
		name string
		args []string
	}{
		{"flatpak", []string{"list", "--app", "--columns=application"}},
		{"snap", []string{"list"}},
		{"apt", []string{"list", "--installed"}},
		{"dnf", []string{"list", "installed", "-q"}},
		{"pacman", []string{"-Qq"}},
		{"zypper", []string{"--no-refresh", "se", "--installed-only", "-t", "package"}},
		{"apk", []string{"info"}},
	}
	for _, manager := range managers {
		if _, err := platformLookPath(manager.name); err != nil {
			continue
		}
		output, err := runner.Run(ctx, manager.name, manager.args...)
		if err == nil && len(strings.TrimSpace(string(output))) > 0 {
			return ParseLinuxPackages(manager.name, string(output)), nil
		}
	}
	return nil, errors.New("no supported Linux package manager is available")
}

func parseWingetExport(contents []byte) ([]Application, error) {
	var exported wingetManifest
	if err := json.Unmarshal(contents, &exported); err != nil {
		return nil, fmt.Errorf("decode winget export: %w", err)
	}
	var applications []Application
	for _, source := range exported.Sources {
		for _, packageInfo := range source.Packages {
			if packageInfo.Identifier == "" {
				return nil, errors.New("winget export contains a package without an identifier")
			}
			applications = append(applications, Application{
				Manager:    "winget",
				Identifier: packageInfo.Identifier,
				Version:    packageInfo.Version,
			})
		}
	}
	return applications, nil
}

func planApplicationInstall(applications []Application) ([]Application, []string) {
	var installable []Application
	var skipped []string
	for _, application := range applications {
		if application.Manager != "winget" || application.Identifier == "" {
			skipped = append(skipped, application.Identifier)
			continue
		}
		installable = append(installable, application)
	}
	return installable, skipped
}

func installWindowsApplications(ctx context.Context, runner CommandRunner, applications []Application, manifestPath string) error {
	if goosFunc() != "windows" {
		return errors.New("Windows application installation requires a Windows destination")
	}
	if _, err := platformLookPath("winget"); err != nil {
		return fmt.Errorf("winget is unavailable: %w", err)
	}
	installable, skipped := planApplicationInstall(applications)
	if len(skipped) > 0 {
		return fmt.Errorf("unsupported application managers or identifiers: %v", skipped)
	}
	if len(installable) == 0 {
		return nil
	}
	packages := make([]wingetPackage, len(installable))
	for index, application := range installable {
		packages[index] = wingetPackage{Identifier: application.Identifier, Version: application.Version}
	}
	contents, err := json.Marshal(wingetManifest{Sources: []wingetSource{{Packages: packages}}})
	if err != nil {
		return fmt.Errorf("encode winget import: %w", err)
	}
	if err := os.WriteFile(manifestPath, contents, 0o600); err != nil {
		return fmt.Errorf("write winget import: %w", err)
	}
	defer os.Remove(manifestPath)
	_, err = runner.Run(ctx, "winget", "import", "--import-file", manifestPath, "--ignore-unavailable", "--accept-package-agreements", "--accept-source-agreements", "--disable-interactivity")
	return err
}
