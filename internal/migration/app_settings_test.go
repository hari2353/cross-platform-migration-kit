package migration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func chromeFixtureHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	userDataDir := chromeUserDataDirForOS(runtime.GOOS, home)
	profileDir := filepath.Join(userDataDir, "Default")
	if err := os.MkdirAll(filepath.Join(profileDir, "Extensions", "cjpalhdlnbpafiamejdnhcphjbkeibjm"), 0o755); err != nil {
		t.Fatal(err)
	}
	bookmarks := []byte("{\"roots\":{\"bookmark_bar\":{\"children\":[{\"name\":\"Quest\",\"url\":\"https://quest.local\"}]},\"other\":{},\"synced\":{}},\"version\":1}")
	if err := os.WriteFile(filepath.Join(profileDir, "Bookmarks"), bookmarks, 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestChromeRecipeCapturesBookmarksNotCredentials(t *testing.T) {
	home := chromeFixtureHome(t)
	setting, checklist, err := captureChromeSetting(home)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(setting.Values["bookmarks"], "quest.local") {
		t.Fatalf("bookmarks missing: %#v", setting.Values)
	}
	if !strings.Contains(setting.Values["extensions"], "cjpalhdlnbpafiamejdnhcphjbkeibjm") {
		t.Fatalf("extension ID missing: %#v", setting.Values)
	}
	if len(checklist) == 0 || !strings.Contains(checklist[0], "Google Sync") {
		t.Fatalf("Chrome checklist missing: %#v", checklist)
	}

	// Restore into a fresh destination home.
	destination := t.TempDir()
	report := NewReport("restore")
	manifest := Manifest{Settings: []Setting{setting}, Checklist: checklist}
	if err := restoreSettings(manifest, destination, &report, false, false, nil); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(filepath.Join(chromeUserDataDirForOS(runtime.GOOS, destination), "Default", "Bookmarks"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(restored), "quest.local") {
		t.Fatalf("unexpected restored bookmarks: %s", restored)
	}
	finishPath := filepath.Join(destination, "FINISH-SETUP.txt")
	if _, err := os.Stat(finishPath); err != nil {
		t.Fatalf("checklist file missing: %v", err)
	}
}

func TestChromeRecipeEmptyWhenChromeAbsent(t *testing.T) {
	setting, checklist, err := captureChromeSetting(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(setting.Values) != 0 || len(checklist) != 0 {
		t.Fatalf("unexpected recipe without Chrome: %#v %#v", setting.Values, checklist)
	}
	if _, _, err := RenderChromeSetting(setting); err == nil {
		t.Fatal("empty Chrome recipe must not render")
	}
}

func TestChromeRecipeRefusesForeignKey(t *testing.T) {
	if _, _, err := RenderChromeSetting(Setting{Recipe: "chrome-v1", Values: map[string]string{"tokens": "x"}}); err == nil {
		t.Fatal("unknown Chrome value accepted")
	}
}

func TestOpenCodeRecipeConfigScrubbedAuthOptIn(t *testing.T) {
	home := t.TempDir()
	configDir := filepath.Join(home, ".config", "opencode")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config := []byte("{\n  \"theme\": \"dark\",\n  \"mcp\": {\"env\": {\"ANTHROPIC_API_KEY\": \"sk-ant-AbCdEfGhIjKlMnOpQrStUvWxYz123456\"}},\n  \"default_agent\": \"build\"\n}")
	if err := os.WriteFile(openCodeConfigPath(home), config, 0o644); err != nil {
		t.Fatal(err)
	}
	authDir := filepath.Join(home, ".local", "share", "opencode")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	auth := []byte("{\"anthropic\":{\"type\":\"api\",\"key\":\"sk-ant-AbCdEfGhIjKlMnOpQrStUvWxYz123456\"}}")
	if err := os.WriteFile(openCodeAuthPath(home), auth, 0o600); err != nil {
		t.Fatal(err)
	}

	// Default: config only, scrubbed, no credentials.
	setting, _, err := captureOpenCodeSetting(home, false)
	if err != nil {
		t.Fatal(err)
	}
	if setting.Values["auth.json"] != "" {
		t.Fatal("auth.json captured without the --credentials opt-in")
	}
	configText := setting.Values["config.json"]
	if strings.Contains(configText, "sk-ant") {
		t.Fatalf("credential survived scrubbing: %s", configText)
	}
	if !strings.Contains(configText, "default_agent") {
		t.Fatalf("legitimate config lost: %s", configText)
	}

	// Opt-in: auth.json travels and restore writes it 0600.
	withAuth, authChecklist, err := captureOpenCodeSetting(home, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(withAuth.Values["auth.json"], "anthropic") {
		t.Fatalf("auth.json missing despite --credentials: %#v", withAuth.Values)
	}
	if len(authChecklist) == 0 || !strings.Contains(authChecklist[0], "opencode models") {
		t.Fatalf("credential checklist missing: %#v", authChecklist)
	}

	destination := t.TempDir()
	report := NewReport("restore")
	manifest := Manifest{Settings: []Setting{withAuth}, Checklist: authChecklist}
	if err := restoreSettings(manifest, destination, &report, false, false, nil); err != nil {
		t.Fatal(err)
	}
	writtenAuth, err := os.Stat(openCodeAuthPath(destination))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && writtenAuth.Mode().Perm() != 0o600 {
		t.Fatalf("auth.json mode %v, want 0600", writtenAuth.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(destination, "FINISH-SETUP.txt")); err != nil {
		t.Fatalf("checklist file missing: %v", err)
	}
}

func TestOpenCodeRecipeRefusesCredentialShapedConfig(t *testing.T) {
	poisoned := Setting{Recipe: "opencode-v1", Values: map[string]string{
		"config.json": "{\"model\":\"claude\",\"key\":\"sk-AbCdEfGhIjKlMnOpQrStUvW\"}",
	}}
	if _, _, err := RenderOpenCodeSetting(poisoned); err == nil {
		t.Fatal("credential-shaped config was accepted for restore")
	}
}

func TestInventoryGateForCredentials(t *testing.T) {
	writeFile := func(root, relative, content string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	makeSource := t.TempDir()
	writeFile(makeSource, ".local/share/opencode/auth.json", "{\"anthropic\":{\"key\":\"sk-FakeKeyAbCdEfGh\"}}")
	writeFile(makeSource, ".ssh/id_ed25519", "private")
	writeFile(makeSource, "Documents/keep.txt", "keep")

	// Default: every credential-like path is excluded.
	items, exclusions, err := inventoryWithOptions(makeSource, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if strings.Contains(item.RelativePath, "auth.json") || strings.Contains(item.RelativePath, "id_ed25519") {
			t.Fatalf("credential traveled by default: %s", item.RelativePath)
		}
	}
	excluded := map[string]Exclusion{}
	for _, exclusion := range exclusions {
		excluded[filepath.ToSlash(exclusion.RelativePath)] = exclusion
	}
	if _, ok := excluded[".local/share/opencode/auth.json"]; !ok {
		t.Fatalf("auth.json not reported as excluded: %#v", exclusions)
	}

	// Opt-in: only the opencode auth.json is exempted; keys stay home.
	items, _, err = inventoryWithOptions(makeSource, t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	sawAuth := false
	for _, item := range items {
		if strings.HasSuffix(item.RelativePath, "opencode/auth.json") {
			sawAuth = true
		}
		if strings.Contains(item.RelativePath, "id_ed25519") {
			t.Fatalf("private key traveled despite gate: %s", item.RelativePath)
		}
	}
	if !sawAuth {
		t.Fatalf("opencode auth.json missing with --credentials: %#v", items)
	}
}

func TestChromeChecklistPersistsAcrossVerify(t *testing.T) {
	// The checklist must survive manifest round-trip through JSON
	// (encrypt/decrypt) so the destination report carries it.
	manifest := Manifest{Checklist: []string{"step one", "step two"}}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Manifest
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Checklist) != 2 || decoded.Checklist[0] != "step one" {
		t.Fatalf("checklist lost in round trip: %#v", decoded.Checklist)
	}
}
