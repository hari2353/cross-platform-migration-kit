package migration

// Application settings capture/restore: Chrome, OpenCode, VS Code, SSH and
// restoration routing. Files here travel as scrubbed recipes; raw copies of
// the same files are excluded by the walk in inventory.go.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

const (
	chromeBookmarksFile = "bookmarks"
	chromeExtensionsKey = "extensions"
	openCodeAuthFile    = "auth.json"
	openCodeConfigFile  = "config.json"
)

// chromeUserDataDirForOS returns the Chrome user data directory for the given OS.
func chromeUserDataDirForOS(goos, home string) string {
	switch goos {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
	case "linux":
		return filepath.Join(home, ".config", "google-chrome")
	case "windows":
		return filepath.Join(home, "AppData", "Local", "Google", "Chrome", "User Data")
	default:
		return filepath.Join(home, ".config", "google-chrome")
	}
}

// openCodeConfigPath returns the path to the OpenCode config file.
func openCodeConfigPath(home string) string {
	return filepath.Join(home, ".config", "opencode", "config.json")
}

// openCodeAuthPath returns the path to the OpenCode auth file.
func openCodeAuthPath(home string) string {
	return filepath.Join(home, ".local", "share", "opencode", "auth.json")
}

// chromeChecklist are the always-on manual steps after a Chrome restore.
var chromeChecklist = []string{"Google Sync: verify Chrome sync settings after restore"}

// chromeExtensionIDs lists the installed Chrome extension directory
// identifiers: 32-character lowercase hex directory names, sorted. A
// non-directory entry or a name that is not a full-length extension id is
// never an identifier.
func chromeExtensionIDs(extensionsDir string) ([]string, error) {
	entries, err := os.ReadDir(extensionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	ids := []string{}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || len(name) != 32 {
			continue
		}
		// Chrome extension directory ids use the 32-character a–p
		// alphabet, not hex.
		valid := true
		for _, character := range name {
			if character < 'a' || character > 'p' {
				valid = false
				break
			}
		}
		if valid {
			ids = append(ids, name)
		}
	}
	return ids, nil
}

// captureChromeSetting reads Chrome's bookmarks database and the installed
// extension identifiers. No Chrome profile at all is an empty recipe; a
// broken or denied profile is a hard error rather than a silent partial.
func captureChromeSetting(home string) (Setting, []string, error) {
	bookmarksPath := filepath.Join(chromeUserDataDirForOS(runtime.GOOS, home), "Default", "Bookmarks")
	bookmarks, err := os.ReadFile(bookmarksPath)
	if err != nil {
		if os.IsNotExist(err) {
			return Setting{}, nil, nil
		}
		return Setting{}, nil, err
	}
	values := map[string]string{chromeBookmarksFile: string(bookmarks)}
	checklist := append([]string{}, chromeChecklist...)
	if ids, err := chromeExtensionIDs(filepath.Join(filepath.Dir(bookmarksPath), "Extensions")); err != nil {
		return Setting{}, nil, err
	} else if len(ids) > 0 {
		values[chromeExtensionsKey] = strings.Join(ids, "\n")
		checklist = append(checklist, "Browser extensions: reinstall any extension that does not sync")
	}
	return Setting{Recipe: "chrome-v1", Values: values}, checklist, nil
}

// RenderChromeSetting re-renders a Chrome recipe into the bookmarks document
// and the extension identifier list; refuses foreign values and recipes that
// carried nothing.
func RenderChromeSetting(setting Setting) ([]byte, []string, error) {
	if setting.Recipe != "chrome-v1" {
		return nil, nil, fmt.Errorf("unknown Chrome recipe: %s", setting.Recipe)
	}
	for key := range setting.Values {
		if key != chromeBookmarksFile && key != chromeExtensionsKey {
			return nil, nil, fmt.Errorf("unknown Chrome value: %s", key)
		}
	}
	extensions := []string{}
	for _, id := range strings.Split(setting.Values[chromeExtensionsKey], "\n") {
		if id = strings.TrimSpace(id); id != "" {
			extensions = append(extensions, id)
		}
	}
	content := []byte(setting.Values[chromeBookmarksFile])
	if len(extensions) == 0 && strings.TrimSpace(string(content)) == "" {
		return nil, nil, errors.New("chrome recipe captured nothing")
	}
	return content, extensions, nil
}

// captureOpenCodeSetting captures the OpenCode config (scrubbed) and, only on
// the explicit opt-in, the auth document. An unparsable config is an empty
// recipe; an unreadable one is a hard error.
func captureOpenCodeSetting(home string, withCredentials bool) (Setting, []string, error) {
	setting := Setting{Recipe: "opencode-v1", Values: map[string]string{}}
	configData, err := os.ReadFile(openCodeConfigPath(home))
	if err != nil && !os.IsNotExist(err) {
		return Setting{}, nil, err
	}
	if len(configData) > 0 {
		scrubbed, excluded := scrubVSCodeSecrets(configData)
		if !jsonValid(scrubbed) {
			return Setting{}, nil, nil
		}
		setting.Values[openCodeConfigFile] = string(scrubbed)
		if len(excluded) > 0 {
			setting.Excluded = append(setting.Excluded, excluded...)
		}
	}

	var checklist []string
	authData, err := os.ReadFile(openCodeAuthPath(home))
	switch {
	case err != nil && os.IsNotExist(err):
		authData = nil
	case err != nil:
		return Setting{}, nil, err
	}
	if withCredentials && len(authData) > 0 {
		setting.Values[openCodeAuthFile] = string(authData)
		checklist = append(checklist, "opencode models: verify model access after restore")
	} else if len(authData) > 0 {
		// Secrets stayed home by default; the destination must sign in fresh.
		checklist = append(checklist, "opencode: sign in on the destination to restore model access")
	}
	return setting, checklist, nil
}

// jsonValid reports whether a document parses as JSON.
func jsonValid(raw []byte) bool {
	return json.Valid(raw)
}

// RenderOpenCodeSetting emits the config bytes and the auth payload; refuses
// foreign recipes, foreign values, and credential-shaped config text.
func RenderOpenCodeSetting(setting Setting) ([]byte, string, error) {
	if setting.Recipe != "opencode-v1" {
		return nil, "", fmt.Errorf("unknown OpenCode recipe: %s", setting.Recipe)
	}
	for key := range setting.Values {
		if key != openCodeConfigFile && key != openCodeAuthFile {
			return nil, "", fmt.Errorf("unknown OpenCode value: %s", key)
		}
	}
	config := setting.Values[openCodeConfigFile]
	if stringContainsCredentialShape(config) {
		return nil, "", errors.New("refusing credential-shaped opencode config")
	}
	return []byte(config), setting.Values[openCodeAuthFile], nil
}

// stringContainsCredentialFreeJSON was removed; see jsonValid.

// RenderSSHConfig re-renders an SSH config recipe; refuses anything but the
// plain config value (private keys always stay home).
func RenderSSHConfig(setting Setting) ([]byte, error) {
	if setting.Recipe != "ssh-config-v1" {
		return nil, fmt.Errorf("unknown SSH recipe: %s", setting.Recipe)
	}
	if len(setting.Values) != 1 {
		return nil, fmt.Errorf("ssh-config-v1 carries exactly one value")
	}
	value, ok := setting.Values["config"]
	if !ok {
		return nil, fmt.Errorf("ssh-config-v1 value must be 'config'")
	}
	return []byte(value), nil
}

// CaptureSSHConfig captures ~/.ssh/config only; keys and agent sockets never travel.
func CaptureSSHConfig(home string) (Setting, error) {
	configPath := filepath.Join(home, ".ssh", "config")
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return Setting{Recipe: "ssh-config-v1", Values: map[string]string{}}, nil
		}
		return Setting{}, err
	}
	return Setting{Recipe: "ssh-config-v1", Values: map[string]string{"config": string(data)}}, nil
}

// userConfigDirFunc is the settings-resolver seam; resolveUserConfigDir is
// the production value tests restore the seam from.
var userConfigDirFunc = resolveUserConfigDir

// resolveUserConfigDir resolves the operator's configuration root,
// preferring the MIGRATE_USER_CONFIG_DIR override so tests and wrappers can
// point capture and restore at a sandbox.
func resolveUserConfigDir() (string, error) {
	if override := os.Getenv("MIGRATE_USER_CONFIG_DIR"); override != "" {
		return override, nil
	}
	return os.UserConfigDir()
}

// vscodeSettingsPath returns the VS Code settings.json path per platform,
// resolved through the user-config seam.
func vscodeSettingsPath(goos string) (string, error) {
	switch goos {
	case "darwin", "linux", "windows":
	default:
		return "", fmt.Errorf("unsupported os: %s", goos)
	}
	configDir, err := userConfigDirFunc()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "Code", "User", "settings.json"), nil
}

// normalizeVSCodeExtensions parses a `code --list-extensions` listing:
// duplicates and malformed identifiers are skipped, real identifiers are
// kept sorted.
func normalizeVSCodeExtensions(raw string) []string {
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || seen[line] || strings.ContainsAny(line, "/\\") {
			continue
		}
		seen[line] = true
		out = append(out, line)
	}
	sortStrings(out)
	return out
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

// captureVSCodeSetting captures the VS Code settings file (scrubbed) plus the
// normalized extension list from the code CLI. An unreadable resolver or an
// unreadable settings file is an empty recipe, never an error.
func captureVSCodeSetting(ctx context.Context, runner CommandRunner) (Setting, error) {
	settingsPath, err := vscodeSettingsPath(goosFunc())
	if err != nil {
		return Setting{Recipe: "vscode-v1", Values: map[string]string{}}, nil
	}
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return Setting{Recipe: "vscode-v1", Values: map[string]string{}}, nil
	}
	scrubbed, excluded := scrubVSCodeSecrets(data)
	values := map[string]string{"settings.json": string(scrubbed)}
	setting := Setting{Recipe: "vscode-v1", Values: values}
	setting.Excluded = append(setting.Excluded, excluded...)
	if runner == nil {
		runner = systemCommandRunner{}
	}
	if _, err := platformLookPath("code"); err == nil {
		if out, err := runner.Run(ctx, "code", "--list-extensions"); err == nil {
			if identifiers := normalizeVSCodeExtensions(string(out)); len(identifiers) > 0 {
				values[chromeExtensionsKey] = strings.Join(identifiers, "\n")
			}
		}
	}
	return setting, nil
}

// RenderVSCodeSetting renders the VS Code recipe into the settings document
// and the normalized extension list; refuses foreign recipes, keys, and
// whitespace-only or NUL-bearing settings.
func RenderVSCodeSetting(setting Setting) ([]byte, []string, error) {
	if setting.Recipe != "vscode-v1" {
		return nil, nil, fmt.Errorf("unknown VS Code recipe: %s", setting.Recipe)
	}
	for key := range setting.Values {
		if key != "settings.json" && key != chromeExtensionsKey {
			return nil, nil, fmt.Errorf("unknown VS Code value: %s", key)
		}
	}
	settings, ok := setting.Values["settings.json"]
	if !ok {
		return nil, nil, errors.New("vscode recipe without settings.json")
	}
	if strings.TrimSpace(settings) == "" {
		return nil, nil, errors.New("whitespace-only settings")
	}
	if strings.Contains(settings, "\x00") {
		return nil, nil, errors.New("NUL byte in settings")
	}
	extensions := []string{}
	for _, id := range strings.Split(setting.Values[chromeExtensionsKey], "\n") {
		if id = strings.TrimSpace(id); id != "" {
			extensions = append(extensions, id)
		}
	}
	return []byte(settings), extensions, nil
}

// installVSCodeExtensions installs listed extensions through the code CLI,
// skipping when the CLI is absent and reporting per-extension failures.
func installVSCodeExtensions(report *Report, extensions []string, runner CommandRunner) {
	if _, err := platformLookPath("code"); err != nil {
		for _, ext := range extensions {
			report.Add(Result{Kind: "extension", ID: ext, Status: "skipped"})
		}
		return
	}
	if runner == nil {
		runner = systemCommandRunner{}
	}
	for _, ext := range extensions {
		if _, err := runner.Run(context.Background(), "code", "--install-extension", ext); err != nil {
			report.Add(Result{Kind: "extension", ID: ext, Status: "failed", Detail: errorString(err)})
			continue
		}
		report.Add(Result{Kind: "extension", ID: ext, Status: "installed"})
	}
}

// restoreSettings replays captured recipes onto a destination home: git
// config, the Chrome profile, OpenCode, SSH, VS Code, and the FINISH-SETUP
// checklist.
func restoreSettings(manifest Manifest, destination string, report *Report, overwrite bool, dryRun bool, runner interface{}) error {
	var codeRunner CommandRunner
	if typed, ok := runner.(CommandRunner); ok {
		codeRunner = typed
	}

	restoreSkipExisting := func(path string) bool {
		if overwrite {
			return false
		}
		if _, statErr := os.Stat(path); statErr == nil {
			return true
		}
		return false
	}

	for _, setting := range manifest.Settings {
		switch setting.Recipe {
		case "git-v1":
			content, err := RenderGitSetting(setting)
			if err != nil {
				report.Add(Result{Kind: "setting", ID: setting.Recipe, Status: "unsupported"})
				continue
			}
			path := filepath.Join(destination, ".gitconfig")
			if restoreSkipExisting(path) {
				report.Add(Result{Kind: "setting", ID: setting.Recipe, Path: path, Status: "skipped-existing"})
				continue
			}
			if err := os.WriteFile(path, content, 0o600); err != nil {
				return err
			}
			report.Add(Result{Kind: "setting", ID: setting.Recipe, Path: path, Status: "restored"})

		case "ssh-config-v1":
			content, err := RenderSSHConfig(setting)
			if err != nil {
				report.Add(Result{Kind: "setting", ID: setting.Recipe, Status: "unsupported"})
				continue
			}
			path := filepath.Join(destination, ".ssh", "config")
			if restoreSkipExisting(path) {
				report.Add(Result{Kind: "setting", ID: setting.Recipe, Path: path, Status: "skipped-existing"})
				continue
			}
			if err := osMkdirAll(filepath.Dir(path), 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(path, content, 0o600); err != nil {
				return err
			}
			report.Add(Result{Kind: "setting", ID: setting.Recipe, Path: path, Status: "restored"})

		case "chrome-v1":
			content, _, err := RenderChromeSetting(setting)
			if err != nil {
				report.Add(Result{Kind: "setting", ID: setting.Recipe, Status: "unsupported"})
				continue
			}
			if strings.TrimSpace(string(content)) == "" {
				continue
			}
			profileDir := filepath.Join(chromeUserDataDirForOS(runtime.GOOS, destination), "Default")
			path := filepath.Join(profileDir, "Bookmarks")
			if restoreSkipExisting(path) {
				report.Add(Result{Kind: "setting", ID: setting.Recipe, Path: path, Status: "skipped-existing"})
				continue
			}
			if err := osMkdirAll(profileDir, 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(path, content, 0o644); err != nil {
				return err
			}
			report.Add(Result{Kind: "setting", ID: setting.Recipe, Path: path, Status: "restored"})

		case "opencode-v1":
			if _, _, renderErr := RenderOpenCodeSetting(setting); renderErr != nil {
				report.Add(Result{Kind: "setting", ID: setting.Recipe, Status: "unsupported"})
				continue
			}
			if cfg := setting.Values[openCodeConfigFile]; cfg != "" {
				path := openCodeConfigPath(destination)
				if restoreSkipExisting(path) {
					report.Add(Result{Kind: "setting", ID: setting.Recipe, Path: path, Status: "skipped-existing"})
				} else {
					if err := osMkdirAll(filepath.Dir(path), 0o755); err != nil {
						return err
					}
					if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
						return err
					}
					report.Add(Result{Kind: "setting", ID: setting.Recipe, Path: path, Status: "restored"})
				}
			}
			if auth := setting.Values[openCodeAuthFile]; auth != "" {
				path := openCodeAuthPath(destination)
				if restoreSkipExisting(path) {
					report.Add(Result{Kind: "credential", ID: openCodeAuthFile, Path: path, Status: "skipped-existing"})
				} else {
					if err := osMkdirAll(filepath.Dir(path), 0o700); err != nil {
						return err
					}
					if err := os.WriteFile(path, []byte(auth), 0o600); err != nil {
						return err
					}
					report.Add(Result{Kind: "credential", ID: openCodeAuthFile, Path: path, Status: "restored"})
				}
			}

		case "vscode-v1":
			content, extensions, err := RenderVSCodeSetting(setting)
			if err != nil {
				report.Add(Result{Kind: "setting", ID: setting.Recipe, Status: "unsupported"})
				continue
			}
			path, err := vscodeSettingsPath(goosFunc())
			if err != nil {
				report.Add(Result{Kind: "setting", ID: setting.Recipe, Status: "unsupported"})
				continue
			}
			if !restoreSkipExisting(path) {
				if err := osMkdirAll(filepath.Dir(path), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(path, content, 0o600); err != nil {
					return err
				}
				report.Add(Result{Kind: "setting", ID: setting.Recipe, Path: path, Status: "restored"})
			} else {
				report.Add(Result{Kind: "setting", ID: setting.Recipe, Path: path, Status: "skipped-existing"})
			}
			if len(extensions) > 0 {
				installVSCodeExtensions(report, extensions, codeRunner)
			}

		case "terminal-v1", "dev-v1", "docker-v1", "kubernetes-v1":
			// Catalog recipes render via the shared dev-file catalog so
			// capture and restore agree byte-for-byte on layout and scrub.
			if err := restoreCatalogSetting(setting, destination, report, overwrite); err != nil {
				return err
			}

		default:
			report.Add(Result{Kind: "setting", ID: setting.Recipe, Status: "unsupported"})
		}
	}
	if len(manifest.Checklist) > 0 {
		finishPath := filepath.Join(destination, "FINISH-SETUP.txt")
		if restoreSkipExisting(finishPath) {
			report.Add(Result{Kind: "checklist", ID: "FINISH-SETUP", Path: finishPath, Status: "skipped-existing"})
		} else {
			lines := make([]string, 0, len(manifest.Checklist))
			for index, step := range manifest.Checklist {
				lines = append(lines, fmt.Sprintf("%d. %s", index+1, step))
			}
			if err := os.WriteFile(finishPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
				return err
			}
			report.Add(Result{Kind: "checklist", ID: "FINISH-SETUP", Path: finishPath, Status: "restored"})
		}
	}
	return nil
}

// isVanishedSource: an error that means "the file disappeared mid-walk" is
// recorded as an unreadable exclusion, never a fatal capture failure.
func isVanishedSource(err error) bool {
	if err == nil {
		return false
	}
	if os.IsNotExist(err) {
		return true
	}
	var pe *os.PathError
	if errors.As(err, &pe) {
		return os.IsNotExist(pe.Err) || errors.Is(pe.Err, syscall.ENOENT)
	}
	return false
}

// credentialsShapeInJSON walks a JSON document structurally and reports
// whether it carries credential-shaped keys or values; an unparsable document
// is conservative and reports a carrier.
func credentialsShapeInJSON(raw []byte) bool {
	var doc interface{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return true
	}
	return jsonCarriesCredential(doc)
}

func jsonCarriesCredential(node interface{}) bool {
	switch value := node.(type) {
	case map[string]interface{}:
		for key, element := range value {
			if isSecretVarName(key) {
				if s, ok := element.(string); !ok || strings.TrimSpace(s) != "" {
					return true
				}
				continue
			}
			if jsonCarriesCredential(element) {
				return true
			}
		}
	case []interface{}:
		for _, element := range value {
			if s, ok := element.(string); ok {
				if stringShapeCredential(s) {
					return true
				}
				continue
			}
			if jsonCarriesCredential(element) {
				return true
			}
		}
	case string:
		if stringShapeCredential(value) {
			return true
		}
	}
	return false
}

// stringShapeCredential matches credential-bearing string values: known
// secret prefixes and URL userinfo credentials.
func stringShapeCredential(value string) bool {
	return stringContainsCredentialShape(value)
}
