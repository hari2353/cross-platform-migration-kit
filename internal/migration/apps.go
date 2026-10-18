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
		}
		plans = append(plans, plan)
	}
	return plans
}

func equivalentApplication(sourceOS, destinationOS string, application Application) (Application, bool) {
	if sourceOS == destinationOS {
		return application, true
	}
	targets, ok := equivalentMappings[mappingKeyFor(application)]
	if !ok {
		return Application{}, false
	}
	for _, target := range targets {
		if targetIsAvailable(destinationOS, target) {
			return target, true
		}
	}
	return Application{}, false
}

// mappingKeyFor scopes curated identifiers by their source manager so identically
// named packages from different adapters never cross-map by accident.
func mappingKeyFor(application Application) string {
	manager := strings.ToLower(application.Manager)
	switch manager {
	case "apt", "dnf", "pacman", "zypper", "apk":
		manager = "linux"
	}
	return manager + ":" + strings.ToLower(application.Identifier)
}

// equivalentMappings is the curated cross-platform application register. Keys are
// "<source-manager>:<lowercase source identifier>" and values are ordered
// destination package intents reviewed manually. Identifiers in this map never
// come from user manifests: the manifest identifiers are lookup keys, and every
// install command is composed from the reviewed values.
var equivalentMappings = map[string][]Application{
	// winget sources. CLI tools map to Homebrew formulas / native Linux
	// packages; GUI applications map to brew casks / Flatpak apps.
	"winget:git.git":                               {{Manager: "brew-brew", Identifier: "git"}, {Manager: "linux", Identifier: "git"}},
	"winget:openjs.nodejs.lts":                     {{Manager: "brew-brew", Identifier: "node"}, {Manager: "linux", Identifier: "nodejs"}},
	"winget:openjs.nodejs":                         {{Manager: "brew-brew", Identifier: "node"}, {Manager: "linux", Identifier: "nodejs"}},
	"winget:microsoft.visualstudiocode":            {{Manager: "brew-cask", Identifier: "visual-studio-code"}, {Manager: "flatpak", Identifier: "com.visualstudio.code"}},
	"winget:google.chrome":                         {{Manager: "brew-cask", Identifier: "google-chrome"}, {Manager: "flatpak", Identifier: "com.google.Chrome"}},
	"winget:brave.brave":                           {{Manager: "brew-cask", Identifier: "brave-browser"}, {Manager: "flatpak", Identifier: "com.brave.Browser"}},
	"winget:mozilla.firefox":                       {{Manager: "brew-cask", Identifier: "firefox"}, {Manager: "flatpak", Identifier: "org.mozilla.firefox"}},
	"winget:videolan.vlc":                          {{Manager: "brew-cask", Identifier: "vlc"}, {Manager: "flatpak", Identifier: "org.videolan.VLC"}},
	"winget:7zip.7zip":                             {{Manager: "brew-brew", Identifier: "p7zip"}, {Manager: "linux", Identifier: "p7zip"}},
	"winget:github.cli":                            {{Manager: "brew-brew", Identifier: "gh"}, {Manager: "linux", Identifier: "gh"}},
	"winget:microsoft.powershell":                  {{Manager: "brew-brew", Identifier: "powershell"}, {Manager: "linux", Identifier: "powershell"}},
	"winget:python.python.3":                       {{Manager: "brew-brew", Identifier: "python"}, {Manager: "linux", Identifier: "python3"}},
	"winget:python.python.3.12":                    {{Manager: "brew-brew", Identifier: "python"}, {Manager: "linux", Identifier: "python3"}},
	"winget:pythonsoftwarefoundation.python.3.12":  {{Manager: "brew-brew", Identifier: "python"}, {Manager: "linux", Identifier: "python3"}},
	"winget:golang.go":                             {{Manager: "brew-brew", Identifier: "go"}, {Manager: "linux", Identifier: "golang"}},
	"winget:rustlang.rustup":                       {{Manager: "brew-brew", Identifier: "rustup"}, {Manager: "linux", Identifier: "rustup"}},
	"winget:docker.dockerdesktop":                  {{Manager: "brew-cask", Identifier: "docker"}, {Manager: "linux", Identifier: "docker"}},
	"winget:kubernetes.kubectl":                    {{Manager: "brew-brew", Identifier: "kubectl"}, {Manager: "linux", Identifier: "kubectl"}},
	"winget:obsproject.obsstudio":                  {{Manager: "brew-cask", Identifier: "obs"}, {Manager: "flatpak", Identifier: "com.obsproject.Studio"}, {Manager: "linux", Identifier: "obs-studio"}},
	"winget:discord.discord":                       {{Manager: "brew-cask", Identifier: "discord"}, {Manager: "flatpak", Identifier: "com.discordapp.Discord"}},
	"winget:slack.slack":                           {{Manager: "brew-cask", Identifier: "slack"}, {Manager: "flatpak", Identifier: "com.slack.Slack"}},
	"winget:telegrammessenger.telegramdesktop":     {{Manager: "brew-cask", Identifier: "telegram"}, {Manager: "flatpak", Identifier: "org.telegram.desktop"}},
	"winget:zoom.zoom":                             {{Manager: "brew-cask", Identifier: "zoom"}, {Manager: "flatpak", Identifier: "us.zoom.Zoom"}},
	"winget:spotify.spotify":                       {{Manager: "brew-cask", Identifier: "spotify"}, {Manager: "flatpak", Identifier: "com.spotify.Client"}},
	"winget:valve.steam":                           {{Manager: "brew-cask", Identifier: "steam"}, {Manager: "flatpak", Identifier: "com.valvesoftware.Steam"}},
	"winget:postman.postman":                       {{Manager: "brew-cask", Identifier: "postman"}, {Manager: "flatpak", Identifier: "com.getpostman.Postman"}},
	"winget:obsidian.obsidian":                     {{Manager: "brew-cask", Identifier: "obsidian"}, {Manager: "flatpak", Identifier: "md.obsidian.Obsidian"}},
	"winget:notion.notion":                         {{Manager: "brew-cask", Identifier: "notion"}},
	"winget:jetbrains.toolbox":                     {{Manager: "brew-cask", Identifier: "jetbrains-toolbox"}},
	"winget:microsoft.sqlservermanagementstudio21": {{Manager: "brew-cask", Identifier: "dbeaver-community"}},
	"winget:ifixit.rufus":                          {{Manager: "brew-cask", Identifier: "balenaetcher"}},
	// Homebrew sources. App Store intents (mas) have no reviewed equivalent and
	// stay unsupported on non-macOS destinations.
	"brew-brew:git":                {{Manager: "winget", Identifier: "Git.Git"}, {Manager: "linux", Identifier: "git"}},
	"brew-brew:node":               {{Manager: "winget", Identifier: "OpenJS.NodeJS.LTS"}, {Manager: "linux", Identifier: "nodejs"}},
	"brew-brew:gh":                 {{Manager: "winget", Identifier: "GitHub.cli"}, {Manager: "linux", Identifier: "gh"}},
	"brew-brew:p7zip":              {{Manager: "winget", Identifier: "7zip.7zip"}, {Manager: "linux", Identifier: "p7zip"}},
	"brew-brew:powershell":         {{Manager: "winget", Identifier: "Microsoft.PowerShell"}, {Manager: "linux", Identifier: "powershell"}},
	"brew-brew:go":                 {{Manager: "winget", Identifier: "Golang.Go"}, {Manager: "linux", Identifier: "golang"}},
	"brew-brew:python":             {{Manager: "winget", Identifier: "Python.Python.3"}, {Manager: "linux", Identifier: "python3"}},
	"brew-brew:rustup":             {{Manager: "winget", Identifier: "Rustlang.Rustup"}, {Manager: "linux", Identifier: "rustup"}},
	"brew-brew:kubectl":            {{Manager: "winget", Identifier: "Kubernetes.Kubectl"}, {Manager: "linux", Identifier: "kubectl"}},
	"brew-cask:visual-studio-code": {{Manager: "winget", Identifier: "Microsoft.VisualStudioCode"}, {Manager: "flatpak", Identifier: "com.visualstudio.code"}},
	"brew-cask:google-chrome":      {{Manager: "winget", Identifier: "Google.Chrome"}, {Manager: "flatpak", Identifier: "com.google.Chrome"}},
	"brew-cask:brave-browser":      {{Manager: "winget", Identifier: "Brave.Brave"}, {Manager: "flatpak", Identifier: "com.brave.Browser"}},
	"brew-cask:firefox":            {{Manager: "winget", Identifier: "Mozilla.Firefox"}, {Manager: "flatpak", Identifier: "org.mozilla.firefox"}},
	"brew-cask:vlc":                {{Manager: "winget", Identifier: "VideoLAN.VLC"}, {Manager: "flatpak", Identifier: "org.videolan.VLC"}},
	"brew-cask:obs":                {{Manager: "winget", Identifier: "OBSProject.OBSStudio"}, {Manager: "linux", Identifier: "obs-studio"}},
	"brew-cask:docker":             {{Manager: "winget", Identifier: "Docker.DockerDesktop"}},
	"brew-cask:discord":            {{Manager: "winget", Identifier: "Discord.Discord"}, {Manager: "flatpak", Identifier: "com.discordapp.Discord"}},
	"brew-cask:slack":              {{Manager: "winget", Identifier: "Slack.Slack"}, {Manager: "flatpak", Identifier: "com.slack.Slack"}},
	"brew-cask:telegram":           {{Manager: "winget", Identifier: "TelegramMessenger.TelegramDesktop"}, {Manager: "flatpak", Identifier: "org.telegram.desktop"}},
	"brew-cask:zoom":               {{Manager: "winget", Identifier: "Zoom.Zoom"}, {Manager: "flatpak", Identifier: "us.zoom.Zoom"}},
	"brew-cask:spotify":            {{Manager: "winget", Identifier: "Spotify.Spotify"}, {Manager: "flatpak", Identifier: "com.spotify.Client"}},
	"brew-cask:steam":              {{Manager: "winget", Identifier: "Valve.Steam"}, {Manager: "flatpak", Identifier: "com.valvesoftware.Steam"}},
	"brew-cask:postman":            {{Manager: "winget", Identifier: "Postman.Postman"}, {Manager: "flatpak", Identifier: "com.getpostman.Postman"}},
	"brew-cask:obsidian":           {{Manager: "winget", Identifier: "Obsidian.Obsidian"}, {Manager: "flatpak", Identifier: "md.obsidian.Obsidian"}},
	"brew-cask:notion":             {{Manager: "winget", Identifier: "Notion.Notion"}},
	"brew-cask:jetbrains-toolbox":  {{Manager: "winget", Identifier: "JetBrains.Toolbox"}},
	// Linux native manager sources.
	"linux:git":        {{Manager: "winget", Identifier: "Git.Git"}, {Manager: "brew-brew", Identifier: "git"}},
	"linux:nodejs":     {{Manager: "winget", Identifier: "OpenJS.NodeJS.LTS"}, {Manager: "brew-brew", Identifier: "node"}},
	"linux:firefox":    {{Manager: "winget", Identifier: "Mozilla.Firefox"}, {Manager: "brew-cask", Identifier: "firefox"}},
	"linux:vlc":        {{Manager: "winget", Identifier: "VideoLAN.VLC"}, {Manager: "brew-cask", Identifier: "vlc"}},
	"linux:gh":         {{Manager: "winget", Identifier: "GitHub.cli"}, {Manager: "brew-brew", Identifier: "gh"}},
	"linux:golang":     {{Manager: "winget", Identifier: "Golang.Go"}, {Manager: "brew-brew", Identifier: "go"}},
	"linux:python3":    {{Manager: "winget", Identifier: "Python.Python.3"}, {Manager: "brew-brew", Identifier: "python"}},
	"linux:powershell": {{Manager: "winget", Identifier: "Microsoft.PowerShell"}, {Manager: "brew-brew", Identifier: "powershell"}},
	"linux:p7zip":      {{Manager: "winget", Identifier: "7zip.7zip"}, {Manager: "brew-brew", Identifier: "p7zip"}},
	"linux:rustup":     {{Manager: "winget", Identifier: "Rustlang.Rustup"}, {Manager: "brew-brew", Identifier: "rustup"}},
	"linux:docker":     {{Manager: "winget", Identifier: "Docker.DockerDesktop"}, {Manager: "brew-cask", Identifier: "docker"}},
	"linux:kubectl":    {{Manager: "winget", Identifier: "Kubernetes.Kubectl"}, {Manager: "brew-brew", Identifier: "kubectl"}},
	"linux:obs-studio": {{Manager: "winget", Identifier: "OBSProject.OBSStudio"}, {Manager: "brew-cask", Identifier: "obs"}},
	"linux:discord":    {{Manager: "winget", Identifier: "Discord.Discord"}, {Manager: "flatpak", Identifier: "com.discordapp.Discord"}},
	"linux:chromium":   {{Manager: "winget", Identifier: "Google.Chrome"}, {Manager: "brew-cask", Identifier: "google-chrome"}},
	"linux:code":       {{Manager: "winget", Identifier: "Microsoft.VisualStudioCode"}, {Manager: "brew-cask", Identifier: "visual-studio-code"}},
	"linux:slack":      {{Manager: "winget", Identifier: "Slack.Slack"}, {Manager: "flatpak", Identifier: "com.slack.Slack"}},
	"linux:telegram":   {{Manager: "winget", Identifier: "TelegramMessenger.TelegramDesktop"}, {Manager: "flatpak", Identifier: "org.telegram.desktop"}},
	"linux:steam":      {{Manager: "winget", Identifier: "Valve.Steam"}, {Manager: "flatpak", Identifier: "com.valvesoftware.Steam"}},
	"linux:spotify":    {{Manager: "winget", Identifier: "Spotify.Spotify"}, {Manager: "flatpak", Identifier: "com.spotify.Client"}},
	// Linux Flatpak and Snap sources.
	"flatpak:com.visualstudio.code":   {{Manager: "winget", Identifier: "Microsoft.VisualStudioCode"}, {Manager: "brew-cask", Identifier: "visual-studio-code"}},
	"flatpak:com.google.chrome":       {{Manager: "winget", Identifier: "Google.Chrome"}, {Manager: "brew-cask", Identifier: "google-chrome"}},
	"flatpak:com.brave.browser":       {{Manager: "winget", Identifier: "Brave.Brave"}, {Manager: "brew-cask", Identifier: "brave-browser"}},
	"flatpak:org.mozilla.firefox":     {{Manager: "winget", Identifier: "Mozilla.Firefox"}, {Manager: "brew-cask", Identifier: "firefox"}},
	"flatpak:org.videolan.vlc":        {{Manager: "winget", Identifier: "VideoLAN.VLC"}, {Manager: "brew-cask", Identifier: "vlc"}},
	"flatpak:com.obsproject.studio":   {{Manager: "winget", Identifier: "OBSProject.OBSStudio"}, {Manager: "brew-cask", Identifier: "obs"}},
	"flatpak:com.discordapp.discord":  {{Manager: "winget", Identifier: "Discord.Discord"}, {Manager: "brew-cask", Identifier: "discord"}},
	"flatpak:com.slack.slack":         {{Manager: "winget", Identifier: "Slack.Slack"}, {Manager: "brew-cask", Identifier: "slack"}},
	"flatpak:org.telegram.desktop":    {{Manager: "winget", Identifier: "TelegramMessenger.TelegramDesktop"}, {Manager: "brew-cask", Identifier: "telegram"}},
	"flatpak:us.zoom.zoom":            {{Manager: "winget", Identifier: "Zoom.Zoom"}, {Manager: "brew-cask", Identifier: "zoom"}},
	"flatpak:com.spotify.client":      {{Manager: "winget", Identifier: "Spotify.Spotify"}, {Manager: "brew-cask", Identifier: "spotify"}},
	"flatpak:com.valvesoftware.steam": {{Manager: "winget", Identifier: "Valve.Steam"}, {Manager: "brew-cask", Identifier: "steam"}},
	"flatpak:com.getpostman.postman":  {{Manager: "winget", Identifier: "Postman.Postman"}, {Manager: "brew-cask", Identifier: "postman"}},
	"flatpak:md.obsidian.obsidian":    {{Manager: "winget", Identifier: "Obsidian.Obsidian"}, {Manager: "brew-cask", Identifier: "obsidian"}},
	"snap:firefox":                    {{Manager: "winget", Identifier: "Mozilla.Firefox"}, {Manager: "brew-cask", Identifier: "firefox"}},
	"snap:vlc":                        {{Manager: "winget", Identifier: "VideoLAN.VLC"}, {Manager: "brew-cask", Identifier: "vlc"}},
	"snap:code":                       {{Manager: "winget", Identifier: "Microsoft.VisualStudioCode"}, {Manager: "brew-cask", Identifier: "visual-studio-code"}},
	"snap:discord":                    {{Manager: "winget", Identifier: "Discord.Discord"}, {Manager: "brew-cask", Identifier: "discord"}},
	"snap:slack":                      {{Manager: "winget", Identifier: "Slack.Slack"}, {Manager: "brew-cask", Identifier: "slack"}},
	"snap:zoom":                       {{Manager: "winget", Identifier: "Zoom.Zoom"}, {Manager: "brew-cask", Identifier: "zoom"}},
	"flatpak:com.slack.Slack":         {{Manager: "winget", Identifier: "Slack.Slack"}, {Manager: "brew-cask", Identifier: "slack"}},
}

// targetIsAvailable keeps the dry-run plan honest for the destination platform:
// a Homebrew/winget/Flatpak capability must exist before an intent can be
// planned, and a Linux native target needs at least one detected manager.
func targetIsAvailable(destinationOS string, target Application) bool {
	switch destinationOS {
	case "darwin":
		return strings.HasPrefix(target.Manager, "brew") || target.Manager == "mas"
	case "windows":
		return target.Manager == "winget"
	case "linux":
		switch target.Manager {
		case "flatpak", "snap":
			return lookPathAvailable(target.Manager)
		case "linux":
			_, ok := nativeManagerName()
			return ok
		}
	}
	return false
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
		if len(fields) >= 2 && fields[0] == "tap" {
			// Tap declarations are repository metadata, not application
			// intents: real `brew bundle dump` output includes them, and
			// dropping the whole inventory over them would break captures.
			continue
		}
		skipped = append(skipped, line)
	}
	return applications, skipped
}

func LinuxInstallCommand(application Application) (ApplicationCommand, bool) {
	if application.Identifier == "" {
		return ApplicationCommand{}, false
	}
	// Each manager has its own non-interactive flag and verb; a shared "-y"
	// silently breaks newer apk ("unrecognized option 'y'") and would never
	// have been valid for pacman or zypper.
	switch application.Manager {
	case "apt", "dnf":
		return ApplicationCommand{Manager: application.Manager, Args: []string{"install", "-y", application.Identifier}}, true
	case "pacman":
		return ApplicationCommand{Manager: "pacman", Args: []string{"install", "--noconfirm", application.Identifier}}, true
	case "zypper":
		return ApplicationCommand{Manager: "zypper", Args: []string{"install", "--non-interactive", application.Identifier}}, true
	case "apk":
		return ApplicationCommand{Manager: "apk", Args: []string{"add", "--no-interactive", application.Identifier}}, true
	case "flatpak":
		return ApplicationCommand{Manager: "flatpak", Args: []string{"install", "-y", application.Identifier}}, true
	case "snap":
		return ApplicationCommand{Manager: "snap", Args: []string{"install", application.Identifier}}, true
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

func applicationCommand(application Application) (ApplicationCommand, bool) {
	switch application.Manager {
	case "brew-brew":
		return ApplicationCommand{Manager: "brew", Args: []string{"install", application.Identifier}}, true
	case "brew-cask":
		return ApplicationCommand{Manager: "brew", Args: []string{"install", "--cask", application.Identifier}}, true
	case "mas":
		return ApplicationCommand{Manager: "mas", Args: []string{"install", application.Identifier}}, true
	case "linux":
		manager, ok := nativeManagerName()
		if !ok {
			return ApplicationCommand{}, false
		}
		return LinuxInstallCommand(Application{Manager: manager, Identifier: application.Identifier})
	default:
		return LinuxInstallCommand(application)
	}
}

// nativeManagerName returns the first native Linux package manager available on
// the destination machine. The order is a stability contract: tests and the
// curated mappings rely on the same preference order as the inventory adapters.
var platformLookPath = func(name string) (string, error) { return exec.LookPath(name) }

func lookPathAvailable(name string) bool {
	_, err := platformLookPath(name)
	return err == nil
}

func nativeManagerName() (string, bool) {
	for _, manager := range []string{"apt", "dnf", "pacman", "zypper", "apk"} {
		if lookPathAvailable(manager) {
			return manager, true
		}
	}
	return "", false
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
		identifier := fields[0]
		if strings.Contains(identifier, "/") && manager != "flatpak" {
			identifier = strings.SplitN(identifier, "/", 2)[0]
		}
		if identifier == "Listing..." {
			continue
		}
		if manager == "flatpak" && len(fields) > 1 {
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

func inventoryApplications(ctx context.Context, runner CommandRunner, tempDir string) ([]Application, error) {
	if runner == nil {
		runner = systemCommandRunner{}
	}
	switch goosFunc() {
	case "windows":
		return inventoryWindowsApplications(ctx, runner, tempDir)
	case "darwin":
		return inventoryDarwinApplications(ctx, runner, tempDir)
	case "linux":
		return inventoryLinuxApplications(ctx, runner)
	default:
		return nil, fmt.Errorf("unsupported source platform %s", goosFunc())
	}
}

func inventoryWindowsApplications(ctx context.Context, runner CommandRunner, tempDir string) ([]Application, error) {
	if runner == nil {
		runner = systemCommandRunner{}
	}
	if !lookPathAvailable("winget") {
		return nil, fmt.Errorf("winget is unavailable")
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

func inventoryDarwinApplications(ctx context.Context, runner CommandRunner, tempDir string) ([]Application, error) {
	if runner == nil {
		runner = systemCommandRunner{}
	}
	if !lookPathAvailable("brew") {
		return nil, fmt.Errorf("brew is unavailable")
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
		// Untranslated lines do not invalidate the intents that were parsed;
		// the caller records the warning while keeping the applications.
		return applications, fmt.Errorf("Brewfile contains unsupported entries: %d", len(skipped))
	}
	return applications, nil
}

// inventoryLinuxApplications merges intents from every detected Linux package
// manager instead of stopping at the first one, so systems that use apt plus
// Flatpak or Snap keep all of their application intents.
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
	var applications []Application
	seen := map[string]bool{}
	available := 0
	for _, manager := range managers {
		if !lookPathAvailable(manager.name) {
			continue
		}
		available++
		output, err := runner.Run(ctx, manager.name, manager.args...)
		if err != nil {
			continue
		}
		for _, application := range ParseLinuxPackages(manager.name, string(output)) {
			key := application.Manager + ":" + application.Identifier
			if !seen[key] {
				applications = append(applications, application)
				seen[key] = true
			}
		}
	}
	if available == 0 {
		return nil, errors.New("no supported Linux package manager is available")
	}
	if len(applications) == 0 {
		return nil, errors.New("package managers were detected but no installed applications could be read")
	}
	return applications, nil
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
	if !lookPathAvailable("winget") {
		return fmt.Errorf("winget is unavailable")
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
	// json.Marshal cannot fail for this primitives-only shape.
	contents, _ := json.Marshal(wingetManifest{Sources: []wingetSource{{Packages: packages}}})
	if err := os.WriteFile(manifestPath, contents, 0o600); err != nil {
		return fmt.Errorf("write winget import: %w", err)
	}
	defer os.Remove(manifestPath)
	_, err := runner.Run(ctx, "winget", "import", "--import-file", manifestPath, "--ignore-unavailable", "--accept-package-agreements", "--accept-source-agreements", "--disable-interactivity")
	return err
}
