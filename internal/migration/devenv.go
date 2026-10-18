package migration

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ---------------------------------------------------------------------------
// Catalog-driven portable coverage: terminal/shell profiles, language runtime
// configuration, Docker, and Kubernetes. Every file captured here is also
// excluded from the raw file inventory (see policyExclusions and
// isCredentialLike additions) so a scrubbed recipe copy is the only copy that
// can travel — a credential embedded in a shell profile must never leak
// through the file layer.

// fileSpec describes one portable file: where it lives per platform, how
// credential material is removed, and what the user must redo on the
// destination when something was dropped.
type fileSpec struct {
	// Key identifies the file inside the recipe Values map.
	Key string
	// Description names the file in reports.
	Description string
	// Paths maps operating systems to a slash-separated path under the
	// user's home. An absent entry means the tool does not use that
	// platform; capture and restore both skip it there.
	Paths map[string]string
	// Scrub sanitizes raw file bytes: content, excluded items, dropped flag.
	// nil means carried verbatim.
	Scrub func(raw []byte) ([]byte, []string, bool)
	// Mode is the destination file mode (0o644 when zero).
	Mode os.FileMode
	// ChecklistWhenScrubbed is emitted at capture only when this file's
	// Scrub dropped something, telling the user what to redo by hand.
	ChecklistWhenScrubbed string
}

// userInfoURIPattern detects credentials embedded in URL userinfo
// (https://user:token@example.tld/...), the shape pip and other registry
// configurations use to hide passwords in plain sight.
var userInfoURIPattern = regexp.MustCompile(`[a-z][a-z0-9+.-]*://[^/@\s]+:[^@/\s]+@`)

// scrubLines drops any line that embeds a credential by shape or by name and
// keeps everything else. It applies to shell profiles, TOML, properties
// files, and other line-oriented configuration.
func scrubLines(raw []byte) ([]byte, []string, bool) {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var kept []string
	var excluded []string
	dropped := false
	for scanner.Scan() {
		line := scanner.Text()
		if lineDropsCredential(line) {
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				excluded = append(excluded, trimmed[:min(60, len(trimmed))])
			}
			dropped = true
			continue
		}
		kept = append(kept, line)
	}
	return []byte(strings.Join(kept, "\n")), excluded, dropped
}

// lineDropsCredential reports whether one configuration line embeds a
// credential by shape (token shapes, URLs with userinfo) or by name
// (token/password/secret/auth-style assignments).
func lineDropsCredential(line string) bool {
	if stringContainsCredentialShape(line) {
		return true
	}
	if userInfoURIPattern.MatchString(line) {
		return true
	}
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return false
	}
	// Name-based guard: exports, assignments, and key/val pairs whose
	// variable or key name matches the secret markers. Whole-word matching
	// keeps benign variables (PATH, certificate-authority-data) intact.
	varname := trimmed
	for _, cut := range []string{"export ", "set ", "setx "} {
		if strings.HasPrefix(strings.ToLower(varname), cut) {
			varname = strings.TrimSpace(varname[len(cut):])
			break
		}
	}
	if index := strings.IndexAny(varname, "=:"); index > 0 {
		varname = varname[:index]
	}
	varname = strings.Trim(strings.TrimSpace(varname), "\"'")
	if varname != "" && isSecretVarName(varname) {
		return true
	}
	return false
}

// isSecretVarName matches variable/key names in line-oriented files
// (shell profiles, properties, YAML). Words are split on non-alphanumerics
// so substring collisions never drop benign names (PATH never matches
// "pat"; certificate-authority-data never matches "auth"), while
// GITHUB_TOKEN, API_KEY, ANTHROPIC_API_KEY and password= all still match.
func isSecretVarName(name string) bool {
	lower := strings.ToLower(name)
	start := 0
	var words []string
	for index := 0; index <= len(lower); index++ {
		if index == len(lower) || lower[index] < 'a' && (lower[index] < '0' || lower[index] > '9') || lower[index] > 'z' && (lower[index] < '0' || lower[index] > '9') {
			if index > start {
				words = append(words, lower[start:index])
			}
			start = index + 1
		}
	}
	for index, word := range words {
		switch word {
		case "token", "secret", "password", "passwd", "credential", "credentials",
			"auth", "authorization", "bearer", "jwt", "cookie", "apikey",
			"privatekey", "pfx", "pat":
			return true
		case "key":
			if index > 0 && (words[index-1] == "api" || words[index-1] == "access" || words[index-1] == "secret" || words[index-1] == "private") {
				return true
			}
			if index+1 < len(words) && words[index+1] == "id" {
				return false
			}
		case "api", "access":
			if index+1 < len(words) && words[index+1] == "key" {
				return true
			}
		}
	}
	return false
}

// scrubJSONGeneric runs the shared two-layer JSON secret scrubber.
func scrubJSONGeneric(raw []byte) ([]byte, []string, bool) {
	scrubbed, excluded := scrubVSCodeSecrets(raw)
	if scrubbed == nil {
		return nil, nil, false
	}
	return scrubbed, excluded, len(excluded) > 0
}

// scrubDockerConfig removes the registry `auths` block (base64 user:password)
// via the generic secret-key pass — "auths" matches the auth marker, so the
// whole block is dropped — while credsStore/credHelpers references and all
// other settings survive.
func scrubDockerConfig(raw []byte) ([]byte, []string, bool) {
	return scrubJSONGeneric(raw)
}

// kubeCredentialKeys are kubeconfig user-entry fields whose values are
// credential material. certificate-authority-data stays: it is public
// cluster metadata needed to connect.
var kubeCredentialKeys = []string{
	"token", "auth-provider", "client-certificate", "client-certificate-data",
	"client-key", "client-key-data", "password", "access-token", "id-token",
	"refresh-token", "impersonate", "as",
}

// scrubKubeConfig removes credential-bearing keys from kubeconfig YAML while
// keeping clusters, contexts, and namespaces intact — the cloud's exec-based
// auth plugins regenerate the removed material at first sign-in.
func scrubKubeConfig(raw []byte) ([]byte, []string, bool) {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var kept []string
	var excluded []string
	dropped := false
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		hit := false
		for _, key := range kubeCredentialKeys {
			if strings.HasPrefix(trimmed, key+":") {
				excluded = append(excluded, key)
				dropped = true
				hit = true
				break
			}
		}
		if hit {
			continue
		}
		if lineDropsCredential(line) {
			if trimmed != "" {
				excluded = append(excluded, trimmed[:min(60, len(trimmed))])
			}
			dropped = true
			continue
		}
		kept = append(kept, line)
	}
	return []byte(strings.Join(kept, "\n")), excluded, dropped
}

func allPlatforms(path string) map[string]string {
	return map[string]string{"windows": path, "darwin": path, "linux": path}
}

// devFileCatalog is the portable developer-environment coverage, shared by
// capture and restore so both sides agree byte-for-byte on where files live
// and how they are sanitized.
var devFileCatalog = []fileSpec{
	// --- Terminal & shell profiles (recipe: terminal-v1) ---
	{Key: ".bashrc", Description: "bash profile", Paths: allPlatforms(".bashrc"), Scrub: scrubLines},
	{Key: ".profile", Description: "login profile", Paths: allPlatforms(".profile"), Scrub: scrubLines},
	{Key: ".bash_aliases", Description: "bash aliases", Paths: allPlatforms(".bash_aliases"), Scrub: scrubLines},
	{Key: ".zshrc", Description: "zsh profile", Paths: allPlatforms(".zshrc"), Scrub: scrubLines},
	{Key: ".zprofile", Description: "zsh login profile", Paths: allPlatforms(".zprofile"), Scrub: scrubLines},
	{Key: ".tmux.conf", Description: "tmux config", Paths: allPlatforms(".tmux.conf"), Scrub: scrubLines},
	{Key: "wt-packaged", Description: "Windows Terminal settings (packaged)",
		Paths: map[string]string{"windows": "AppData/Local/Packages/Microsoft.WindowsTerminal_8wekyb3d8bbwe/LocalState/settings.json"},
		Scrub: scrubJSONGeneric},
	{Key: "wt-unpackaged", Description: "Windows Terminal settings",
		Paths: map[string]string{"windows": "AppData/Local/Microsoft/Windows Terminal/settings.json"},
		Scrub: scrubJSONGeneric},
	{Key: "ps-profile-win71", Description: "PowerShell 7 profile (Windows)",
		Paths: map[string]string{"windows": "Documents/PowerShell/Microsoft.PowerShell_profile.ps1"},
		Scrub: scrubLines},
	{Key: "ps-profile-win7a", Description: "PowerShell 7 all-hosts profile (Windows)",
		Paths: map[string]string{"windows": "Documents/PowerShell/profile.ps1"},
		Scrub: scrubLines},
	{Key: "ps-profile-win51", Description: "Windows PowerShell 5 profile",
		Paths: map[string]string{"windows": "Documents/WindowsPowerShell/Microsoft.PowerShell_profile.ps1"},
		Scrub: scrubLines},
	{Key: "ps-profile-unix1", Description: "PowerShell 7 profile (macOS/Linux)",
		Paths: map[string]string{"darwin": ".config/powershell/Microsoft.PowerShell_profile.ps1", "linux": ".config/powershell/Microsoft.PowerShell_profile.ps1"},
		Scrub: scrubLines},
	{Key: "ps-profile-unixa", Description: "PowerShell 7 all-hosts profile (macOS/Linux)",
		Paths: map[string]string{"darwin": ".config/powershell/profile.ps1", "linux": ".config/powershell/profile.ps1"},
		Scrub: scrubLines},
	{Key: "alacritty", Description: "alacritty config",
		Paths: allPlatforms(".config/alacritty/alacritty.toml"), Scrub: scrubLines},
	{Key: "kitty", Description: "kitty config",
		Paths: allPlatforms(".config/kitty/kitty.conf"), Scrub: scrubLines},

	// --- Language runtimes (recipe: dev-v1) ---
	{Key: ".npmrc", Description: "npm config", Paths: allPlatforms(".npmrc"), Scrub: scrubLines, Mode: 0o600,
		ChecklistWhenScrubbed: "npm: registry token removed from .npmrc — run 'npm login' on the new machine."},
	{Key: ".yarnrc", Description: "yarn config", Paths: allPlatforms(".yarnrc"), Scrub: scrubLines, Mode: 0o600,
		ChecklistWhenScrubbed: "yarn: registry token removed from .yarnrc — run 'yarn npm login' on the new machine."},
	{Key: "pip-conf", Description: "pip config",
		Paths: map[string]string{
			"windows": "AppData/Roaming/pip/pip.ini",
			"darwin":  ".config/pip/pip.conf",
			"linux":   ".config/pip/pip.conf",
		},
		Scrub: scrubLines, Mode: 0o600,
		ChecklistWhenScrubbed: "pip: index credentials removed from pip config — re-authenticate on the new machine."},
	{Key: "cargo-config", Description: "cargo config",
		Paths: allPlatforms(".cargo/config.toml"), Scrub: scrubLines, Mode: 0o600,
		ChecklistWhenScrubbed: "cargo: registry token removed from .cargo/config.toml — run 'cargo login' on the new machine."},
	{Key: "go-env", Description: "go environment",
		Paths: map[string]string{
			"windows": "AppData/Roaming/go/env",
			"darwin":  ".config/go/env",
			"linux":   ".config/go/env",
		},
		Scrub: scrubLines, Mode: 0o600},
	{Key: ".tool-versions", Description: "asdf tool versions",
		Paths: allPlatforms(".tool-versions"), Scrub: scrubLines},
	{Key: ".python-version", Description: "python version marker",
		Paths: allPlatforms(".python-version"), Scrub: scrubLines},
	{Key: ".node-version", Description: "node version marker",
		Paths: allPlatforms(".node-version"), Scrub: scrubLines},
	{Key: ".nvmrc", Description: "nvm node marker",
		Paths: allPlatforms(".nvmrc"), Scrub: scrubLines},
	{Key: ".ruby-version", Description: "ruby version marker",
		Paths: allPlatforms(".ruby-version"), Scrub: scrubLines},
	{Key: "maven-settings", Description: "maven settings",
		Paths: allPlatforms(".m2/settings.xml"), Scrub: scrubLines,
		ChecklistWhenScrubbed: "maven: credentials removed from ~/.m2/settings.xml — re-add them via your password manager."},
	{Key: "gradle-properties", Description: "gradle properties",
		Paths: allPlatforms(".gradle/gradle.properties"), Scrub: scrubLines, Mode: 0o600,
		ChecklistWhenScrubbed: "gradle: credential properties removed from gradle.properties — re-add them via your password manager."},

	// --- Docker (recipe: docker-v1) ---
	{Key: "docker-config", Description: "docker client config",
		Paths: allPlatforms(".docker/config.json"), Scrub: scrubDockerConfig, Mode: 0o600,
		ChecklistWhenScrubbed: "docker: registry logins removed from .docker/config.json — run 'docker login' on the new machine."},
	{Key: "docker-desktop-win", Description: "Docker Desktop settings (Windows)",
		Paths: map[string]string{"windows": "AppData/Roaming/Docker/settings.json"},
		Scrub: scrubJSONGeneric},
	{Key: "docker-desktop-mac", Description: "Docker Desktop settings (macOS)",
		Paths: map[string]string{"darwin": "Library/Group Containers/group.com.docker/settings.json"},
		Scrub: scrubJSONGeneric},

	// --- Kubernetes (recipe: kubernetes-v1) ---
	{Key: "kube-config", Description: "kubeconfig",
		Paths: allPlatforms(".kube/config"), Scrub: scrubKubeConfig, Mode: 0o600,
		ChecklistWhenScrubbed: "kubernetes: credential entries dropped from .kube/config (token/cert data) — run your cloud login ('az login' / 'gcloud auth login' / 'aws sso login') on the new machine."},
}

// recipeForSpec maps catalog keys to their owning recipe.
func recipeForSpec(key string) string {
	switch key {
	case ".bashrc", ".profile", ".bash_aliases", ".zshrc", ".zprofile", ".tmux.conf",
		"wt-packaged", "wt-unpackaged",
		"ps-profile-win71", "ps-profile-win7a", "ps-profile-win51",
		"ps-profile-unix1", "ps-profile-unixa",
		"alacritty", "kitty":
		return "terminal-v1"
	case "docker-config", "docker-desktop-win", "docker-desktop-mac":
		return "docker-v1"
	case "kube-config":
		return "kubernetes-v1"
	default:
		return "dev-v1"
	}
}

// captureDevSettings captures every catalog file found under the source home
// and returns one Setting per owning recipe plus checklist entries for files
// whose credentials had to be dropped.
func captureDevSettings(home string) ([]Setting, []string, error) {
	byRecipe := map[string]*Setting{}
	var checklist []string
	for _, spec := range devFileCatalog {
		recipe := recipeForSpec(spec.Key)
		setting, ok := byRecipe[recipe]
		if !ok {
			setting = &Setting{Recipe: recipe, Values: map[string]string{}}
			byRecipe[recipe] = setting
		}
		relative, ok := spec.Paths[goosFunc()]
		if !ok {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(home, filepath.FromSlash(relative)))
		if err != nil {
			if errIsNotExist(err) {
				continue
			}
			return nil, nil, err
		}
		content := raw
		var excluded []string
		if spec.Scrub != nil {
			content, excluded, _ = spec.Scrub(trimByteOrderMark(raw))
			if content == nil {
				// Unparseable beyond repair: capture nothing rather than
				// guess, and say so.
				setting.Excluded = append(setting.Excluded, spec.Key+": unparsable file skipped")
				continue
			}
		}
		setting.Values[spec.Key] = string(content)
		if len(excluded) > 0 {
			for _, item := range excluded {
				setting.Excluded = append(setting.Excluded, spec.Key+": "+item)
			}
			if spec.ChecklistWhenScrubbed != "" {
				checklist = append(checklist, spec.ChecklistWhenScrubbed)
			}
		}
	}
	settings := []Setting{}
	for _, recipe := range []string{"terminal-v1", "dev-v1", "docker-v1", "kubernetes-v1"} {
		if setting, ok := byRecipe[recipe]; ok && len(setting.Values) > 0 {
			settings = append(settings, *setting)
		}
	}
	return settings, checklist, nil
}

// restoreCatalogSetting writes recipe-owned catalog files onto the
// destination home at per-destination-platform paths, with skip-existing
// semantics and a re-scrub at restore time so an old bundle can never
// re-emit credential material.
func restoreCatalogSetting(setting Setting, destination string, report *Report, overwrite bool) error {
	for _, spec := range devFileCatalog {
		if recipeForSpec(spec.Key) != setting.Recipe {
			continue
		}
		value, ok := setting.Values[spec.Key]
		if !ok {
			continue
		}
		relative, ok := spec.Paths[goosFunc()]
		if !ok {
			report.Add(Result{Kind: "setting", ID: setting.Recipe, Status: "platform-skipped", Detail: spec.Description})
			continue
		}
		path := filepath.Join(destination, filepath.FromSlash(relative))
		if _, statErr := os.Stat(path); statErr == nil && !overwrite {
			report.Add(Result{Kind: "setting", ID: setting.Recipe, Path: path, Status: "skipped-existing"})
			continue
		}
		content := []byte(value)
		if spec.Scrub != nil {
			scrubbed, excluded, dropped := spec.Scrub(trimByteOrderMark(content))
			if dropped && scrubbed != nil {
				content = scrubbed
				report.Add(Result{Kind: "sanitized", ID: setting.Recipe, Status: "re-checked",
					Detail: fmt.Sprintf("%s: %d credential entries dropped again at restore", spec.Description, len(excluded))})
			}
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		mode := spec.Mode
		if mode == 0 {
			mode = 0o644
		}
		if err := os.WriteFile(path, content, mode); err != nil {
			return err
		}
		report.Add(Result{Kind: "setting", ID: setting.Recipe, Path: path, Status: "restored"})
	}
	return nil
}
