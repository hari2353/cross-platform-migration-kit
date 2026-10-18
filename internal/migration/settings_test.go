package migration

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGitSettingCapturesOnlyPortableIdentity(t *testing.T) {
	home := t.TempDir()
	gitconfig := "\xEF\xBB\xBF[user]\n\tname = Example User\n\temail = user@example.com\n[credential]\n\thelper = store\n"
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(gitconfig), 0o600); err != nil {
		t.Fatal(err)
	}
	setting, err := CaptureGitSetting(home)
	if err != nil {
		t.Fatal(err)
	}
	if setting.Values["name"] != "Example User" || setting.Values["email"] != "user@example.com" {
		t.Fatalf("BOM broke identity capture: %#v", setting.Values)
	}
	if _, ok := setting.Values["helper"]; ok {
		t.Fatal("credential helper was captured")
	}
}

func TestGitSettingRejectsUnknownValues(t *testing.T) {
	if _, err := RenderGitSetting(Setting{Recipe: "git-v1", Values: map[string]string{"token": "secret"}}, "windows", "darwin"); err == nil {
		t.Fatal("secret setting was accepted")
	}
}

func TestGitSettingCapturesAliasesAndBranchPolicy(t *testing.T) {
	home := t.TempDir()
	config := "[user]\n\tname = Example User\n\temail = user@example.com\n" +
		"[init]\n\tdefaultBranch = main\n" +
		"[core]\n\tautocrlf = true\n" +
		"[alias]\n\tst = status -sb\n    lg = log --oneline --graph --decorate\n" +
		"[credential \"https://example.com\"]\n\thelper = manager\n"
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	setting, err := CaptureGitSetting(home)
	if err != nil {
		t.Fatal(err)
	}
	if setting.Values["defaultbranch"] != "main" || setting.Values["autocrlf"] != "true" {
		t.Fatalf("unexpected values: %#v", setting.Values)
	}
	if setting.Values["alias.st"] != "status -sb" || setting.Values["alias.lg"] != "log --oneline --graph --decorate" {
		t.Fatalf("unexpected aliases: %#v", setting.Values)
	}
	// Subsection headers must not leak their values.
	for key := range setting.Values {
		if strings.Contains(key, "helper") {
			t.Fatalf("subsection value captured: %#v", setting.Values)
		}
	}

	// Windows -> macOS: autocrlf true becomes input, aliases are preserved.
	content, err := RenderGitSetting(setting, "windows", "darwin")
	if err != nil {
		t.Fatal(err)
	}
	rendered := string(content)
	if !strings.Contains(rendered, "autocrlf = input") {
		t.Fatalf("autocrlf was not transformed for a non-Windows destination: %s", rendered)
	}
	if !strings.Contains(rendered, "defaultBranch = main") || !strings.Contains(rendered, "st = status -sb") {
		t.Fatalf("expected portable keys in rendered config: %s", rendered)
	}

	// Windows -> Windows: nothing to transform.
	same, err := RenderGitSetting(setting, "windows", "windows")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(same), "autocrlf = true") {
		t.Fatalf("same-platform destination should keep autocrlf: %s", same)
	}
}

func TestSSHConfigRecipeCapturesConfigOnly(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte("Host dev\n  HostName 10.0.0.5\n  User deploy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	setting, err := CaptureSSHConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(setting.Values["config"], "HostName 10.0.0.5") {
		t.Fatalf("missing config content: %#v", setting.Values)
	}
	content, err := RenderSSHConfig(setting)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "Host dev") {
		t.Fatalf("unexpected render: %s", content)
	}
	// Missing file yields an empty recipe, not an error.
	empty, err := CaptureSSHConfig(t.TempDir())
	if err != nil || len(empty.Values) != 0 {
		t.Fatalf("absent ssh config should be empty: %#v %v", empty.Values, err)
	}
}

func TestVSCodeRecipeScrubsSecretsAndCapturesExtensions(t *testing.T) {
	original := platformLookPath
	t.Cleanup(func() { platformLookPath = original })

	extensionRunner := &scriptedRunner{responses: map[string][]scriptedResponse{}}
	setting, _ := captureVSCodeForTest(t, extensionRunner, true, "")

	content, extensions, err := RenderVSCodeSetting(setting)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	if strings.Contains(text, "super-secret-token") {
		t.Fatalf("secret survived scrubbing: %s", text)
	}
	if strings.Contains(text, "ghp_") {
		t.Fatalf("credential-shaped value survived scrubbing: %s", text)
	}
	if !strings.Contains(text, "editor.fontFamily") {
		t.Fatalf("legitimate settings were lost: %s", text)
	}
	if !containsExcludedKey(setting, "api.supertoken") || !containsExcludedKey(setting, "auth.gh.pat") {
		t.Fatalf("excluded key names not recorded: %#v", setting.Excluded)
	}
	if !strings.Contains(setting.Values["extensions"], "ms-python.python") {
		t.Fatalf("extensions missing: %#v", setting.Values["extensions"])
	}
	if len(extensions) != 2 || extensions[0] != "ms-python.python" || extensions[1] != "ms-vscode.cpptools" {
		t.Fatalf("unexpected extension list: %#v", extensions)
	}
}

func TestRestoreSettingsWritesEveryRecipeWithPlatformTransform(t *testing.T) {
	destination := t.TempDir()
	configRoot := t.TempDir()
	originalConfigDir := userConfigDirFunc
	t.Cleanup(func() { userConfigDirFunc = originalConfigDir })
	userConfigDirFunc = func() (string, error) { return configRoot, nil }

	manifest := Manifest{
		SourceOS: "windows",
		Settings: []Setting{
			{Recipe: "git-v1", Values: map[string]string{
				"name": "Example User", "email": "user@example.com",
				"defaultbranch": "main", "autocrlf": "true",
				"alias.st": "status -sb",
			}},
			{Recipe: "ssh-config-v1", Values: map[string]string{"config": "Host dev\n  HostName 10.0.0.5\n"}},
			{Recipe: "vscode-v1", Values: map[string]string{
				"settings.json": "{\n\t\"editor.fontFamily\": \"JetBrainsMono\"\n}",
				"extensions":    "ms-python.python",
			}},
			{Recipe: "unknown-v1", Values: map[string]string{}},
		},
	}
	report := NewReport("restore")
	if err := restoreSettings(manifest, destination, &report, false, false, nil); err != nil {
		t.Fatal(err)
	}

	gitconfig, err := os.ReadFile(filepath.Join(destination, ".gitconfig"))
	if err != nil {
		t.Fatal(err)
	}
	gitText := string(gitconfig)
	// windows -> windows keeps autocrlf; a non-Windows destination host
	// transforms it to "input" (covered by the render test above).
	wantAutocrlf := "autocrlf = true"
	if runtime.GOOS != "windows" {
		wantAutocrlf = "autocrlf = input"
	}
	for _, want := range []string{"name = Example User", "defaultBranch = main", "st = status -sb", wantAutocrlf} {
		if !strings.Contains(gitText, want) {
			t.Fatalf(".gitconfig missing %q:\n%s", want, gitText)
		}
	}

	sshConfig, err := os.ReadFile(filepath.Join(destination, ".ssh", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if string(sshConfig) != "Host dev\n  HostName 10.0.0.5\n" {
		t.Fatalf("unexpected ssh config: %q", sshConfig)
	}

	vscodePath, err := vscodeSettingsPath(runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	vscodeContent, err := os.ReadFile(vscodePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(vscodeContent), "JetBrainsMono") {
		t.Fatalf("unexpected VS Code settings: %s", vscodeContent)
	}

	statuses := map[string]int{}
	for _, result := range report.Results {
		statuses[result.Kind+"/"+result.Status]++
	}
	_ = statuses
	foundUnsupported := false
	for _, result := range report.Results {
		if result.Kind == "setting" && result.Status == "unsupported" && result.ID == "unknown-v1" {
			foundUnsupported = true
		}
	}
	if !foundUnsupported {
		t.Fatalf("unknown recipe was not reported as unsupported: %#v", report.Results)
	}
}

func TestVSCodeRecipeRetainsEmptyRecordWithoutSettings(t *testing.T) {
	originalConfigDir := userConfigDirFunc
	t.Cleanup(func() { userConfigDirFunc = originalConfigDir })
	userConfigDirFunc = func() (string, error) { return t.TempDir(), nil }

	originalLook := platformLookPath
	t.Cleanup(func() { platformLookPath = originalLook })
	platformLookPath = func(string) (string, error) { return "/usr/bin/code", nil }

	setting, err := captureVSCodeSetting(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(setting.Values) != 0 {
		t.Fatalf("unexpected values without settings file: %#v", setting.Values)
	}
	if _, _, err := RenderVSCodeSetting(setting); err == nil {
		t.Fatal("empty recipe must not render")
	}
}

type scriptedRunner struct {
	responses map[string][]scriptedResponse
	commands  [][]string
}

type scriptedResponse struct {
	output []byte
	err    error
}

func (runner *scriptedRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	runner.commands = append(runner.commands, append([]string{name}, args...))
	queued := runner.responses[name]
	if len(queued) == 0 {
		return []byte("ms-python.python\nms-vscode.cpptools\n"), nil
	}
	response := queued[0]
	runner.responses[name] = queued[1:]
	return response.output, response.err
}

func captureVSCodeForTest(t *testing.T, runner CommandRunner, withCLI bool, prefix string) (Setting, string) {
	t.Helper()
	originalLook := platformLookPath
	t.Cleanup(func() { platformLookPath = originalLook })
	if withCLI {
		platformLookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	} else {
		platformLookPath = func(string) (string, error) { return "", os.ErrNotExist }
	}

	configRoot := t.TempDir()
	originalConfigDir := userConfigDirFunc
	t.Cleanup(func() { userConfigDirFunc = originalConfigDir })
	userConfigDirFunc = func() (string, error) { return configRoot, nil }

	target := filepath.Join(configRoot, "Code", "User")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	content := captureVSCodeSettingsContentForScrubTest()
	if prefix != "" {
		content = prefix + content
	}
	settingsPath := filepath.Join(target, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	setting, err := captureVSCodeSetting(context.Background(), runner)
	if err != nil {
		t.Fatal(err)
	}
	return setting, setting.Values["settings.json"]
}

func captureVSCodeSettingsContentForScrubTest() string {
	return "{\n" +
		"  \"editor.fontFamily\": \"JetBrainsMono\",\n" +
		"  \"remote.SSH.defaultExtensions\": [],\n" +
		"  \"api.supertoken\": \"super-secret-token\",\n" +
		"  \"auth.gh.pat\": \"ghp_AbCdEfGhIjKlMnOpQrStUvWxYz1234567890a\"\n" +
		"}"
}

func TestVSCodeRecipeHandlesByteOrderMarkAndInvalidJSON(t *testing.T) {
	originalLook := platformLookPath
	t.Cleanup(func() { platformLookPath = originalLook })
	platformLookPath = func(string) (string, error) { return "", os.ErrNotExist }

	bombed, text := captureVSCodeForTest(t, nil, true, "\uFEFF")
	if !strings.Contains(text, "editor.fontFamily") {
		t.Fatalf("BOM broke settings capture: %q", text)
	}
	if strings.Contains(strings.Join(bombed.Excluded, ","), "editor.fontFamily") {
		t.Fatalf("legitimate key was excluded: %#v", bombed.Excluded)
	}
	if !containsExcludedKey(bombed, "api.supertoken") {
		t.Fatalf("secret key was not recorded as excluded: %#v", bombed.Excluded)
	}

	invalid, text := captureVSCodeForTest(t, nil, true, "not json at all")
	_ = invalid
	// Invalid JSON captures nothing rather than guessing.
	if _, _, err := RenderVSCodeSetting(invalid); err == nil {
		t.Fatalf("invalid input JSON must not render, got %q", text)
	}
}

func containsExcludedKey(setting Setting, key string) bool {
	for _, name := range setting.Excluded {
		if name == key {
			return true
		}
	}
	return false
}
