package migration

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGitSettingCapturesOnlyPortableIdentity(t *testing.T) {
	home := t.TempDir()
	gitconfig := "[user]\n\tname = Example User\n\temail = user@example.com\n[credential]\n\thelper = store\n"
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(gitconfig), 0o600); err != nil {
		t.Fatal(err)
	}
	setting, err := CaptureGitSetting(home)
	if err != nil {
		t.Fatal(err)
	}
	if setting.Values["name"] != "Example User" || setting.Values["email"] != "user@example.com" {
		t.Fatalf("unexpected setting: %#v", setting)
	}
	if _, ok := setting.Values["helper"]; ok {
		t.Fatal("credential helper was captured")
	}
}

func TestGitSettingRejectsUnknownValues(t *testing.T) {
	if _, err := RenderGitSetting(Setting{Recipe: "git-v1", Values: map[string]string{"token": "secret"}}); err == nil {
		t.Fatal("secret setting was accepted")
	}
}
