package migration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

type Setting struct {
	Recipe   string            `json:"recipe"`
	Values   map[string]string `json:"values"`
	Excluded []string          `json:"excluded,omitempty"`
}

// ---------------------------------------------------------------------------
// Git

// CaptureGitSetting records the reviewed portable subset of .gitconfig:
// identity, init.defaultBranch, core.autocrlf, and alias.X entries.
// credential helpers, tokens in URLs, and unknown keys are excluded.
func CaptureGitSetting(home string) (Setting, error) {
	setting := Setting{Recipe: "git-v1", Values: map[string]string{}}
	config, err := os.Open(filepath.Join(home, ".gitconfig"))
	if err != nil {
		if errIsNotExist(err) {
			return setting, nil
		}
		return Setting{}, err
	}
	defer config.Close()
	section := ""
	scanner := bufio.NewScanner(config)
	for scanner.Scan() {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") || strings.HasPrefix(text, ";") {
			continue
		}
		// Editors on Windows commonly save .gitconfig with a UTF-8 byte order
		// mark; drop it so the first section header still parses.
		text = strings.TrimPrefix(text, string([]byte{0xEF, 0xBB, 0xBF}))
		if strings.HasPrefix(text, "[") && strings.HasSuffix(text, "]") {
			header := strings.Trim(text, "[]")
			// Subsections such as [remote "origin"] are never portable config.
			if strings.Contains(header, "\"") {
				section = ""
				continue
			}
			section = strings.ToLower(strings.TrimSpace(header))
			continue
		}
		parts := strings.SplitN(text, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(parts[0]))
		value := strings.TrimSpace(parts[1])
		switch section {
		case "user":
			if key == "name" || key == "email" {
				setting.Values[key] = value
			}
		case "init":
			if key == "defaultbranch" {
				setting.Values["defaultbranch"] = value
			}
		case "core":
			if key == "autocrlf" {
				setting.Values["autocrlf"] = value
			}
		case "alias":
			aliasName := strings.TrimSpace(parts[0])
			if aliasName != "" && value != "" && !isSecretKeyName(aliasName) {
				setting.Values["alias."+aliasName] = value
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return Setting{}, err
	}
	return setting, nil
}

// RenderGitSetting renders the reviewed portable keys and transforms
// core.autocrlf through the destination platform template: "true" (Windows
// checkout normalization) becomes "input" (checkout as committed) on non-Windows
// destinations, and "input" becomes "true" for Windows destinations.
func RenderGitSetting(setting Setting, sourceOS, destinationOS string) ([]byte, error) {
	if setting.Recipe != "git-v1" {
		return nil, fmt.Errorf("unsupported Git recipe")
	}
	var aliases []string
	for key := range setting.Values {
		if !strings.HasPrefix(key, "alias.") && key != "name" && key != "email" && key != "defaultbranch" && key != "autocrlf" {
			return nil, fmt.Errorf("Git recipe contains an unsupported value %q", key)
		}
	}
	out := []byte("[user]\n\tname = " + setting.Values["name"] + "\n\temail = " + setting.Values["email"] + "\n")
	if setting.Values["defaultbranch"] != "" {
		out = append(out, []byte("[init]\n\tdefaultBranch = "+setting.Values["defaultbranch"]+"\n")...)
	}
	if raw := setting.Values["autocrlf"]; raw != "" {
		transformed := raw
		switch {
		case destinationOS != "windows" && strings.EqualFold(raw, "true"):
			transformed = "input"
		case destinationOS == "windows" && strings.EqualFold(raw, "input"):
			transformed = "true"
		}
		out = append(out, []byte("[core]\n\tautocrlf = "+transformed+"\n")...)
	}
	for key := range setting.Values {
		if strings.HasPrefix(key, "alias.") {
			aliases = append(aliases, key[len("alias."):])
		}
	}
	if len(aliases) > 0 {
		sort.Strings(aliases)
		out = append(out, []byte("[alias]\n")...)
		for _, alias := range aliases {
			out = append(out, []byte("\t"+alias+" = "+setting.Values["alias."+alias]+"\n")...)
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// SSH config (never keys or known_hosts)

// CaptureSSHConfig records ~/.ssh/config, which is portable configuration.
// Private keys, public keys, and known_hosts remain excluded by the file
// inventory policy; the recipe copies only the host configuration text.
func CaptureSSHConfig(home string) (Setting, error) {
	setting := Setting{Recipe: "ssh-config-v1", Values: map[string]string{}}
	config, err := os.ReadFile(filepath.Join(home, ".ssh", "config"))
	if err != nil {
		if errIsNotExist(err) {
			return setting, nil
		}
		return Setting{}, err
	}
	setting.Values["config"] = string(config)
	return setting, nil
}

// RenderSSHConfig returns the reviewed SSH host configuration verbatim; the
// recipe never carries key material, so there is nothing to transform.
func RenderSSHConfig(setting Setting) ([]byte, error) {
	if setting.Recipe != "ssh-config-v1" {
		return nil, fmt.Errorf("unsupported SSH config recipe")
	}
	if len(setting.Values) > 1 {
		return nil, fmt.Errorf("SSH config recipe contains unsupported values")
	}
	return []byte(setting.Values["config"]), nil
}

// ---------------------------------------------------------------------------
// VS Code

// vscodeSecretKeyMarkers lists the key-name fragments isSecretKeyName applies
// with substring matching, so the list errs wide: "auths" picks up the Docker
// registry block, "apikey" covers API/API_KEY names. "pat" is deliberately
// absent — it false-positives on benign "path"/"patch" keys, and credential
// values under such keys remain covered by the value-shape layer.
var vscodeSecretKeyMarkers = []string{
	"token", "secret", "password", "credential", "apikey", "api_key",
	"privatekey", "private_key", "authorization", "bearer", "jwt", "pfx",
	"auth",
}

// vscodeSecretValuePatterns are credential shapes that must not leave the
// source machine even when the key name alone does not look secret-shaped
// (for example "auth.gh.pat": "ghp_..."). Matching keys are dropped whole;
// a false positive costs one re-copied setting, a false negative leaks a
// real credential, so the block list errs wide.
var vscodeSecretValuePatterns = []*regexp.Regexp{
	regexp.MustCompile(`github_pat_[0-9A-Za-z_]{10,}`),
	regexp.MustCompile(`ghp_[0-9A-Za-z]{20,}`),
	regexp.MustCompile(`gho_[0-9A-Za-z]{20,}`),
	regexp.MustCompile(`sk-[0-9A-Za-z_-]{20,}`),
	regexp.MustCompile(`xox[bpars]-[0-9A-Za-z-]{10,}`),
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	regexp.MustCompile(`Bearer\s+[0-9A-Za-z._-]{20,}`),
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`),
}

// stringContainsCredentialShape reports whether a string value looks like a
// machine-generated credential.
func stringContainsCredentialShape(value string) bool {
	for _, pattern := range vscodeSecretValuePatterns {
		if pattern.MatchString(value) {
			return true
		}
	}
	return false
}

// isSecretKeyName classifies JSON key names with substring matching: the
// block list errs wide on purpose ("auths" catches the Docker registry block,
// "api.supertoken" catches "token" as a suffix). "pat" is deliberately absent
// from vscodeSecretKeyMarkers because it false-positives on benign "path" and
// "patch" keys; credential-shaped "pat" values are still caught by the
// value-shape layer (stringContainsCredentialShape).
func isSecretKeyName(name string) bool {
	lower := strings.ToLower(name)
	for _, marker := range vscodeSecretKeyMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// vscodeUserSettingsPaths maps operating systems to the default VS Code
// settings.json location relative to that platform's user configuration root.
var vscodeUserSettingsPaths = map[string][]string{
	"windows": {"Code", "User", "settings.json"},
	"darwin":  {"Code", "User", "settings.json"},
	"linux":   {"Code", "User", "settings.json"},
}

// userConfigDirFunc returns the per-user configuration root. It is a variable
// so tests can redirect VS Code reads and writes to a temporary directory.
// MIGRATE_USER_CONFIG_DIR overrides the platform default for the same reason.
var userConfigDirFunc = resolveUserConfigDir

// goosFunc reports the running platform. Tests override it to execute
// darwin/linux code paths (profile locations, per-platform lists, restore
// transforms) on any host; production always sees runtime.GOOS.
var goosFunc = func() string { return runtime.GOOS }

func resolveUserConfigDir() (string, error) {
	if override := os.Getenv("MIGRATE_USER_CONFIG_DIR"); override != "" {
		return override, nil
	}
	return os.UserConfigDir()
}

func captureVSCodeSetting(ctx context.Context, runner CommandRunner) (Setting, error) {
	setting := Setting{Recipe: "vscode-v1", Values: map[string]string{}}
	configRoot, err := userConfigDirFunc()
	if err != nil {
		return setting, nil
	}
	relPath, ok := vscodeUserSettingsPaths[goosFunc()]
	if !ok {
		return setting, nil
	}
	settingsFile := filepath.Join(append([]string{configRoot}, relPath...)...)
	raw, err := os.ReadFile(settingsFile)
	if err != nil {
		if !errIsNotExist(err) {
			return setting, nil
		}
		return setting, nil
	}
	raw = trimByteOrderMark(raw)
	scrubbed, excluded := scrubVSCodeSecrets(raw)
	if len(scrubbed) > 0 {
		setting.Values["settings.json"] = string(scrubbed)
		if len(excluded) > 0 {
			setting.Excluded = excluded
		}
	}
	if lookPathAvailable("code") {
		if runner == nil {
			runner = systemCommandRunner{}
		}
		if output, runErr := runner.Run(ctx, "code", "--list-extensions"); runErr == nil {
			setting.Values["extensions"] = normalizeVSCodeExtensions(string(output))
		}
	}
	return setting, nil
}

// trimByteOrderMark removes a UTF-8 BOM so Windows-authored JSON and
// configuration files parse the same as POSIX-authored ones.
func trimByteOrderMark(raw []byte) []byte {
	return bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
}

// scrubVSCodeSecrets removes any key (at any depth) whose name matches the
// secret markers and returns the sanitized JSON plus the dropped key names.
func scrubVSCodeSecrets(raw []byte) ([]byte, []string) {
	var document map[string]interface{}
	if err := json.Unmarshal(trimByteOrderMark(raw), &document); err != nil {
		// Not valid JSON: capture nothing rather than guessing.
		return nil, nil
	}
	var excluded []string
	scrubbed := scrubVSCodeValue(document, &excluded, "")
	// json.MarshalIndent cannot fail for a JSON-decoded value shape.
	encoded, _ := json.MarshalIndent(scrubbed, "", "\t")
	return encoded, excluded
}

func scrubVSCodeValue(value interface{}, excluded *[]string, prefix string) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		clean := map[string]interface{}{}
		for key, child := range typed {
			keyPath := strings.TrimPrefix(prefix+"."+key, ".")
			if isSecretKeyName(key) || stringContainsCredentialShape(vscodeValueText(child)) {
				*excluded = append(*excluded, keyPath)
				continue
			}
			clean[key] = scrubVSCodeValue(child, excluded, keyPath)
		}
		return clean
	case []interface{}:
		clean := make([]interface{}, 0, len(typed))
		for _, child := range typed {
			clean = append(clean, scrubVSCodeValue(child, excluded, prefix))
		}
		return clean
	default:
		return value
	}
}

// vsCodeValueText flattens a JSON node to text so credential-shape checks can
// also see values hidden in nested objects and arrays.
func vscodeValueText(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []interface{}:
		parts := make([]string, 0, len(typed))
		for _, child := range typed {
			parts = append(parts, vscodeValueText(child))
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

func normalizeVSCodeExtensions(output string) string {
	seen := map[string]bool{}
	lines := strings.Fields(output)
	normalized := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || seen[line] || strings.ContainsAny(line, " \t/") {
			continue
		}
		seen[line] = true
		normalized = append(normalized, line)
	}
	sort.Strings(normalized)
	return strings.Join(normalized, "\n")
}

// RenderVSCodeSetting validates and returns the captured settings.json.
func RenderVSCodeSetting(setting Setting) ([]byte, []string, error) {
	if setting.Recipe != "vscode-v1" {
		return nil, nil, fmt.Errorf("unsupported VS Code recipe")
	}
	if _, ok := setting.Values["settings.json"]; !ok {
		return nil, nil, fmt.Errorf("VS Code recipe is missing settings.json")
	}
	if strings.TrimSpace(setting.Values["settings.json"]) == "" {
		return nil, nil, fmt.Errorf("VS Code settings were unreadable at capture time")
	}
	for key := range setting.Values {
		if key != "settings.json" && key != "extensions" {
			return nil, nil, fmt.Errorf("VS Code recipe contains an unsupported value %q", key)
		}
	}
	if strings.Contains(setting.Values["settings.json"], "\x00") {
		return nil, nil, fmt.Errorf("VS Code settings contain invalid characters")
	}
	var extensions []string
	if raw := strings.TrimSpace(setting.Values["extensions"]); raw != "" {
		extensions = strings.Split(raw, "\n")
	}
	return []byte(setting.Values["settings.json"]), extensions, nil
}

// vscodeSettingsPath returns where settings.json belongs on the destination
// platform: os.UserConfigDir covers the Windows %AppData%, macOS
// ~/Library/Application Support, and Linux ~/.config locations equally.
func vscodeSettingsPath(destinationOS string) (string, error) {
	if _, ok := vscodeUserSettingsPaths[destinationOS]; !ok {
		return "", fmt.Errorf("unsupported destination platform for VS Code settings")
	}
	configRoot, err := userConfigDirFunc()
	if err != nil {
		return "", err
	}
	return filepath.Join(configRoot, "Code", "User", "settings.json"), nil
}

// installVSCodeExtensions installs the captured extension identifiers through
// the "code" CLI when it exists; missing CLI support is reported, never fatal.
func installVSCodeExtensions(report *Report, extensions []string, runner CommandRunner) {
	if !lookPathAvailable("code") {
		for _, identifier := range extensions {
			report.Add(Result{Kind: "extension", ID: identifier, Status: "skipped", Detail: "the VS Code CLI (code) is not on PATH"})
		}
		return
	}
	for _, identifier := range extensions {
		if runner == nil {
			runner = systemCommandRunner{}
		}
		output, err := runner.Run(context.Background(), "code", "--install-extension", identifier, "--force")
		_ = output
		if err != nil {
			report.Add(Result{Kind: "extension", ID: identifier, Status: "failed", Detail: err.Error()})
			continue
		}
		report.Add(Result{Kind: "extension", ID: identifier, Status: "installed"})
	}
}

// ---------------------------------------------------------------------------
// Chrome (bookmarks + extension list; passwords stay behind, see checklist)

// chromeUserDataDirForOS maps a user home to that platform's Chrome
// "User Data" directory; both capture (source home) and restore
// (destination home) compose from the same relative layout.
func chromeUserDataDirForOS(goos, home string) string {
	switch goos {
	case "windows":
		return filepath.Join(home, "AppData", "Local", "Google", "Chrome", "User Data")
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
	default:
		return filepath.Join(home, ".config", "google-chrome")
	}
}

// BookmarksFile is Chrome's on-disk bookmarks database; JSON, no secrets.
const chromeBookmarksFile = "bookmarks"

// chromeChecklistLine records the credentials reality honestly: Chrome
// passwords are encrypted with DPAPI/app-bound keys on Windows and cannot be
// carried by file copy to any other machine — Chrome Sync is the supported,
// user-consented path.
const chromeChecklistLine = "Chrome: sign in with your Google account on the new machine. Google Sync restores passwords, sessions, and extensions automatically; Windows password files are bound to that machine by Chrome's own encryption and are never copied."

// captureChromeSetting records Chrome bookmarks and the installed extension
// identifiers of the Default profile. Password stores, cookies, and caches
// stay excluded (protected-path policy); the recipe carries only what makes
// Chrome feel familiar before the user signs in.
func captureChromeSetting(home string) (Setting, []string, error) {
	setting := Setting{Recipe: "chrome-v1", Values: map[string]string{}}
	userDataDir := chromeUserDataDirForOS(goosFunc(), home)
	bookmarks, err := os.ReadFile(filepath.Join(userDataDir, "Default", "Bookmarks"))
	if err != nil {
		if !errIsNotExist(err) {
			return Setting{}, nil, err
		}
		return setting, nil, nil // Chrome not present → empty recipe
	}
	setting.Values[chromeBookmarksFile] = string(bookmarks)
	extensionIDs, err := chromeExtensionIDs(filepath.Join(userDataDir, "Default", "Extensions"))
	if err != nil {
		return Setting{}, nil, err
	}
	if len(extensionIDs) > 0 {
		setting.Values["extensions"] = strings.Join(extensionIDs, "\n")
	}
	checklist := []string{chromeChecklistLine}
	if len(extensionIDs) > 0 {
		checklist = append(checklist, fmt.Sprintf("Chrome: %d browser extensions reinstalled automatically when you sign in to your Google account (Sync).", len(extensionIDs)))
	}
	return setting, checklist, nil
}

// chromeExtensionIDs lists installed extension directory identifiers
// (Chrome uses 32-character a-p hex IDs).
var chromeExtensionIDPattern = regexp.MustCompile(`^[a-p][a-p]{31}$`)

func chromeExtensionIDs(extensionsDir string) ([]string, error) {
	entries, err := os.ReadDir(extensionsDir)
	if err != nil {
		if errIsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	ids := []string{}
	for _, entry := range entries {
		if entry.IsDir() && chromeExtensionIDPattern.MatchString(entry.Name()) {
			ids = append(ids, entry.Name())
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// RenderChromeSetting validates and returns the bookmarks database plus the
// captured extension identifier list.
func RenderChromeSetting(setting Setting) ([]byte, []string, error) {
	if setting.Recipe != "chrome-v1" {
		return nil, nil, fmt.Errorf("unsupported Chrome recipe")
	}
	for key := range setting.Values {
		if key != chromeBookmarksFile && key != "extensions" {
			return nil, nil, fmt.Errorf("Chrome recipe contains an unsupported value %q", key)
		}
	}
	if _, hasBookmarks := setting.Values[chromeBookmarksFile]; !hasBookmarks && strings.TrimSpace(setting.Values["extensions"]) == "" {
		return nil, nil, fmt.Errorf("Chrome recipe captured nothing")
	}
	if content, ok := setting.Values[chromeBookmarksFile]; ok {
		if strings.TrimSpace(content) == "" {
			return nil, nil, fmt.Errorf("Chrome bookmarks database is empty")
		}
	}
	var extensions []string
	if raw := strings.TrimSpace(setting.Values["extensions"]); raw != "" {
		extensions = strings.Split(raw, "\n")
	}
	return []byte(setting.Values[chromeBookmarksFile]), extensions, nil
}

// ---------------------------------------------------------------------------
// OpenCode (config always; auth.json only with explicit --credentials opt-in)

const (
	openCodeConfigFile = "config.json"
	openCodeAuthFile   = "auth.json"
)

// opencode stores its global config under ~/.config/opencode on every
// platform and its provider credentials under ~/.local/share/opencode.
func openCodeConfigPath(home string) string {
	return filepath.Join(home, ".config", "opencode", "opencode.json")
}

func openCodeAuthPath(home string) string {
	return filepath.Join(home, ".local", "share", "opencode", "auth.json")
}

// captureOpenCodeSetting records the OpenCode global configuration (scrubbed
// with the same secret-name / credential-shape rules as VS Code settings).
// auth.json holds plain-text provider API keys owned by the user: it travels
// only when the operator passes --credentials, an explicit, recorded,
// owner-approved exception to the "never copy credentials" default.
func captureOpenCodeSetting(home string, includeCredentials bool) (Setting, []string, error) {
	setting := Setting{Recipe: "opencode-v1", Values: map[string]string{}}
	raw, err := os.ReadFile(openCodeConfigPath(home))
	if err != nil && !errIsNotExist(err) {
		return Setting{}, nil, err
	}
	if err == nil {
		scrubbed, excluded := scrubVSCodeSecrets(trimByteOrderMark(raw))
		if len(scrubbed) > 0 {
			setting.Values[openCodeConfigFile] = string(scrubbed)
			if len(excluded) > 0 {
				setting.Excluded = excluded
			}
		}
	}
	checklist := []string{}
	if includeCredentials {
		auth, err := os.ReadFile(openCodeAuthPath(home))
		if err != nil && !errIsNotExist(err) {
			return Setting{}, nil, err
		}
		if err == nil {
			setting.Values[openCodeAuthFile] = string(auth)
			checklist = append(checklist, "opencode: provider credentials were restored with the --credentials opt-in; run 'opencode models' to confirm access.")
		}
	}
	if len(setting.Values) > 0 && setting.Values[openCodeAuthFile] == "" {
		checklist = append(checklist, "opencode: run 'opencode auth login' to reconnect your provider accounts (credentials are not copied by default).")
	}
	return setting, checklist, nil
}

// RenderOpenCodeSetting validates the recipe and returns the configuration
// document plus the credential document (empty string when it did not
// travel).
func RenderOpenCodeSetting(setting Setting) ([]byte, string, error) {
	if setting.Recipe != "opencode-v1" {
		return nil, "", fmt.Errorf("unsupported OpenCode recipe")
	}
	for key := range setting.Values {
		if key != openCodeConfigFile && key != openCodeAuthFile {
			return nil, "", fmt.Errorf("OpenCode recipe contains an unsupported value %q", key)
		}
	}
	if config, ok := setting.Values[openCodeConfigFile]; ok && strings.TrimSpace(config) != "" {
		// Re-check the stored configuration: a document that contains a
		// credential shape must never be re-emitted, even from an old bundle.
		if credentialsShapeInJSON([]byte(config)) {
			return nil, "", fmt.Errorf("OpenCode configuration contains credential-shaped values")
		}
	}
	return []byte(setting.Values[openCodeConfigFile]), setting.Values[openCodeAuthFile], nil
}

func credentialsShapeInJSON(raw []byte) bool {
	var document map[string]interface{}
	if err := json.Unmarshal(trimByteOrderMark(raw), &document); err != nil {
		return true // unparsable JSON is treated as never-portable
	}
	return walkJSONForCredentialShape(document, "")
}

func walkJSONForCredentialShape(value interface{}, prefix string) bool {
	switch typed := value.(type) {
	case map[string]interface{}:
		for key, child := range typed {
			keyPath := strings.TrimPrefix(prefix+"."+key, ".")
			if isSecretKeyName(key) || stringContainsCredentialShape(vscodeValueText(child)) {
				return true
			}
			if walkJSONForCredentialShape(child, keyPath) {
				return true
			}
		}
	case []interface{}:
		for _, child := range typed {
			if walkJSONForCredentialShape(child, prefix) {
				return true
			}
		}
	case string:
		return stringContainsCredentialShape(typed)
	}
	return false
}

func errIsNotExist(err error) bool {
	return err != nil && os.IsNotExist(err)
}
