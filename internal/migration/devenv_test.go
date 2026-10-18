package migration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeDevFixture(t *testing.T, home string, relative, content string) {
	t.Helper()
	path := filepath.Join(home, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestScrubLinesDropsCredentialsKeepsAliases(t *testing.T) {
	raw := []byte(strings.Join([]string{
		"alias ll='ls -al'",
		"export PATH=\"$HOME/bin:$PATH\"",
		"export GITHUB_TOKEN=ghp_AbCdEfGhIjKlMnOpQrStUvWxYzZy0z123456",
		"ANTHROPIC_API_KEY=sk-ant-FakeKeyAbCdEfGhIjKlMnOpQrStUvWxYz",
		"export PYPI_INDEX=https://user:sup3rsecret@example.tld/simple",
		"# harmless comment",
	}, "\n"))
	scrubbed, excluded, dropped := scrubLines(raw)
	if !dropped {
		t.Fatal("no credential reported as dropped")
	}
	text := string(scrubbed)
	for _, must := range []string{"alias ll", "export PATH", "# harmless comment"} {
		if !strings.Contains(text, must) {
			t.Fatalf("legit line lost: %s (in %q)", must, text)
		}
	}
	for _, banned := range []string{"ghp_", "sk-ant", "sup3rsecret"} {
		if strings.Contains(text, banned) {
			t.Fatalf("credential survived: %q", text)
		}
	}
	if len(excluded) < 3 {
		t.Fatalf("expected >=3 exclusions, got %#v", excluded)
	}
}

func TestScrubDockerConfigEmptiesAuthsKeepsHelpers(t *testing.T) {
	raw := []byte(`{"auths":{"index.docker.io":{"auth":"dXNlcjpwYXNzd29yZA=="}},
"credsStore":"desktop","psFormat":"table {{.Image}}"}`)
	scrubbed, excluded, dropped := scrubDockerConfig(raw)
	if !dropped {
		t.Fatal("auths not reported dropped")
	}
	var document map[string]interface{}
	if err := json.Unmarshal(scrubbed, &document); err != nil {
		t.Fatalf("scrubbed docker config unparsable: %v", err)
	}
	auths, ok := document["auths"].(map[string]interface{})
	if ok && len(auths) != 0 {
		t.Fatalf("auths not emptied/removed: %#v", document["auths"])
	}
	if document["credsStore"] != "desktop" {
		t.Fatalf("credsStore lost: %#v", document)
	}
	if len(excluded) == 0 {
		t.Fatalf("exclusion list wrong: %#v", excluded)
	}
	if _, err := json.MarshalIndent(document, "", "  "); err != nil {
		t.Fatalf("scrubbed docker config re-encodes broken: %v", err)
	}
}

func TestScrubKubeConfigDropsCredentialKeysKeepsClusters(t *testing.T) {
	raw := []byte(strings.Join([]string{
		"apiVersion: v1",
		"clusters:",
		"- cluster:",
		    "  server: https://kubernetes.example.tld",
		    "  certificate-authority-data: UBLIC",
		"contexts:",
		"- context:",
		    "  cluster: prod",
		    "  user: dev@example.tld",
		"current-context: prod",
		"users:",
		"- name: dev@example.tld",
		"  user:",
		    "  token: eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9FAKE",
		    "  client-certificate-data: BASE64CERTDATAFAKE",
		    "  client-key-data: BASE64KEYDATAFAKE",
	}, "\n"))
	scrubbed, excluded, dropped := scrubKubeConfig(raw)
	if !dropped {
		t.Fatal("kube credentials not dropped")
	}
	text := string(scrubbed)
	for _, must := range []string{"https://kubernetes.example.tld", "current-context: prod", "certificate-authority-data"} {
		if !strings.Contains(text, must) {
			t.Fatalf("cluster context lost: %s (in %q)", must, text)
		}
	}
	for _, banned := range []string{"eyJ", "BASE64CERTDATAFAKE", "BASE64KEYDATAFAKE", "token:"} {
		if strings.Contains(text, banned) {
			t.Fatalf("credential survived kubeconfig: %q", text)
		}
	}
	if len(excluded) < 3 {
		t.Fatalf("expected >=3 exclusions, got %#v", excluded)
	}
}

func devFixtureHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	writeDevFixture(t, home, ".bashrc", strings.Join([]string{
		"alias build='make all'",
		"export GITHUB_TOKEN=ghp_AbCdEfGhIjKlMnOpQrStUvWxYzZy0z123456",
	}, "\n"))
	writeDevFixture(t, home, ".zshrc", "setopt autocd\nalias g=git\n")
	writeDevFixture(t, home, ".npmrc", "registry=https://registry.npmjs.org/\n_authToken=ghp_AbCdEfGhIjKlMnOpQrStUvWxYzZy0z123456\n")
	writeDevFixture(t, home, ".docker/config.json", `{"auths":{"index.docker.io":{"auth":"dXNlcjpwYXNzd29yZA=="}},"credsStore":"desktop"}`)
	writeDevFixture(t, home, ".kube/config", strings.Join([]string{
		"apiVersion: v1",
		"current-context: prod",
		"clusters:",
		"- cluster:",
		"    server: https://prod.example.tld",
		"contexts:",
		"- context:",
		"    cluster: prod",
		"users:",
		"- name: prod",
		"  user:",
		"    token: SECRETBEARERTOKEN1234567890",
	}, "\n"))
	writeDevFixture(t, home, ".tool-versions", "nodejs 22.4.0\npython 3.12.2\n")
	return home
}

func homePath(relative string) string {
	return filepath.FromSlash(relative)
}

func TestDevRoundTripCaptureRestoreScrubbed(t *testing.T) {
	home := devFixtureHome(t)
	settings, checklist, err := captureDevSettings(home)
	if err != nil {
		t.Fatal(err)
	}
	recipes := map[string]Setting{}
	for _, setting := range settings {
		recipes[setting.Recipe] = setting
	}
	if _, ok := recipes["terminal-v1"]; !ok {
		t.Fatalf("terminal recipe missing: %#v", recipes)
	}
	bashrc := recipes["terminal-v1"].Values[".bashrc"]
	if !strings.Contains(bashrc, "make all") || strings.Contains(bashrc, "ghp_") {
		t.Fatalf(".bashrc scrub wrong: %q", bashrc)
	}
	npmrc := recipes["dev-v1"].Values[".npmrc"]
	if !strings.Contains(npmrc, "registry.npmjs.org") || strings.Contains(npmrc, "_authToken") {
		t.Fatalf(".npmrc scrub wrong: %q", npmrc)
	}
	var docker map[string]interface{}
	if err := json.Unmarshal([]byte(recipes["docker-v1"].Values["docker-config"]), &docker); err != nil {
		t.Fatal(err)
	}
	auths, haveAuths := docker["auths"].(map[string]interface{})
	if haveAuths && len(auths) != 0 {
		t.Fatalf("docker auths not scrubbed: %#v", auths)
	}
	kube := recipes["kubernetes-v1"].Values["kube-config"]
	if strings.Contains(kube, "SECRETBEARERTOKEN") || !strings.Contains(kube, "prod.example.tld") {
		t.Fatalf("kube scrub wrong: %q", kube)
	}
	checklistText := strings.Join(checklist, "\n")
	for _, want := range []string{"npm login", "docker login", "cloud login"} {
		if !strings.Contains(checklistText, want) {
			t.Fatalf("checklist missing %q: %#v", want, checklist)
		}
	}

	// Restore into a fresh destination with the same test on each platform.
	destination := t.TempDir()
	report := NewReport("restore")
	for _, setting := range settings {
		if err := restoreCatalogSetting(setting, destination, &report, false); err != nil {
			t.Fatal(err)
		}
	}
	home2 := destination
	restoredBashrc, err := os.ReadFile(filepath.Join(home2, homePath(".bashrc")))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(restoredBashrc), "ghp_") || !strings.Contains(string(restoredBashrc), "make all") {
		t.Fatalf("restored .bashrc wrong: %q", restoredBashrc)
	}
	restoredNpmrc, err := os.ReadFile(filepath.Join(home2, homePath(".npmrc")))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(restoredNpmrc), "_authToken") {
		t.Fatalf("restored .npmrc kept credentials: %q", restoredNpmrc)
	}
	restoredToolVersions, err := os.ReadFile(filepath.Join(home2, homePath(".tool-versions")))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(restoredToolVersions), "nodejs 22.4.0") {
		t.Fatalf("restored .tool-versions wrong: %q", restoredToolVersions)
	}
	if _, err := os.Stat(filepath.Join(home2, homePath(".kube/config"))); err != nil {
		t.Fatalf("kubeconfig missing: %v", err)
	}
	// skip-existing respected on second restore.
	report2 := NewReport("restore")
	for _, setting := range settings {
		if err := restoreCatalogSetting(setting, destination, &report2, false); err != nil {
			t.Fatal(err)
		}
	}
	skipped := 0
	for _, entry := range report2.Results {
		if entry.Status == "skipped-existing" {
			skipped++
		}
	}
	if skipped == 0 {
		t.Fatal("second restore should skip existing files")
	}
}

func TestInventoryNeverRawCarriesCatalogFiles(t *testing.T) {
	source := t.TempDir()
	repository := t.TempDir()
	writeDevFixture(t, source, ".bashrc", "alias x=1\nexport SECRET_TOKEN=TopSecretValue123\n")
	writeDevFixture(t, source, ".npmrc", "_authToken=ghp_AbCdEfGhIjKlMnOpQrStUvWxYzZy0z123456\n")
	writeDevFixture(t, source, ".docker/config.json", `{"auths":{"a.u":{"auth":"dA=="}}}`)
	writeDevFixture(t, source, ".kube/config", "apiVersion: v1\nusers:\n- name: u\n  user:\n    token: SECRETBEARERTOKEN1234567890\n")
	writeDevFixture(t, source, ".tool-versions", "nodejs 22\n")
	writeDevFixture(t, source, "Documents/keep.txt", "keep")

	items, exclusions, err := inventoryWithOptions(source, repository, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		switch filepath.ToSlash(item.RelativePath) {
		case ".bashrc", ".npmrc", ".docker/config.json", ".kube/config", ".tool-versions":
			t.Fatalf("catalog file traveled raw: %s", item.RelativePath)
		}
	}
	sawKeep := false
	for _, item := range items {
		if item.RelativePath == filepath.ToSlash(filepath.Join("Documents", "keep.txt")) {
			sawKeep = true
		}
	}
	if !sawKeep {
		t.Fatalf("Documents file missing from items: %#v", items)
	}
	for _, target := range []string{".bashrc", ".npmrc", ".docker", ".kube"} {
		found := false
		for _, exclusion := range exclusions {
			if filepath.ToSlash(exclusion.RelativePath) == target || strings.HasPrefix(filepath.ToSlash(exclusion.RelativePath), target+"/") {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("exclusion missing for %s", target)
		}
	}
}

// The catalog must keep the general-coverage targets the owner asked for:
// shells, language runtimes, Docker, and Kubernetes.
var devRecipeCatalogKeys = []string{".bashrc", ".zshrc", ".npmrc", "docker-config", "kube-config", ".tool-versions", "go-env"}

func TestCatalogCoversGeneralTargets(t *testing.T) {
	keys := map[string]bool{}
	for _, spec := range devFileCatalog {
		keys[spec.Key] = true
		if spec.Scrub == nil {
			t.Fatalf("spec %s must define a scrubber", spec.Key)
		}
		if spec.Paths == nil || len(spec.Paths) == 0 {
			t.Fatalf("spec %s has no platform paths", spec.Key)
		}
	}
	for _, key := range devRecipeCatalogKeys {
		if !keys[key] {
			t.Fatalf("catalog lost %s", key)
		}
	}
}
