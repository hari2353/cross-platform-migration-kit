package migration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackPortableDriveCreatesOneClickLaunchers(t *testing.T) {
	bundle := t.TempDir()
	// A fake repository marker; pack only needs the format + manifest markers.
	if err := os.WriteFile(filepath.Join(bundle, manifestFile), []byte{0}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, formatFile), []byte(formatVersion), 0o600); err != nil {
		t.Fatal(err)
	}
	binaries := t.TempDir()
	keySource := filepath.Join(t.TempDir(), "key-from-source")
	if err := os.WriteFile(keySource, []byte("012345678901234567890123456789012"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Every platform may not be present in a binaries dir; two stand-ins sound.
	for _, name := range []string{"migrate-darwin-arm64", "migrate-linux-amd64", "migrate-windows-amd64.exe"} {
		if err := os.WriteFile(filepath.Join(binaries, name), []byte("binary-payload:"+name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	written, err := PackPortableDrive(bundle, keySource, binaries)
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	names := map[string]bool{}
	for _, path := range written {
		names[filepath.Base(path)] = true
	}
	for _, required := range []string{portableKeyFile, portableLauncherPOSX, portableLauncherMac, portableLauncherWin, portableReadmeFile, "migrate-darwin-arm64", "migrate-linux-amd64", "migrate-windows-amd64.exe"} {
		if !names[required] {
			t.Fatalf("expected %s among written files, got %#v", required, names)
		}
	}

	launcher, err := os.ReadFile(filepath.Join(bundle, portableLauncherPOSX))
	if err != nil {
		t.Fatal(err)
	}
	script := string(launcher)
	if !strings.HasPrefix(script, "#!/bin/sh") {
		t.Fatalf("launcher must start with a POSIX shebang, got %q", script[:16])
	}
	if strings.Contains(script, "\r") {
		t.Fatal("POSIX launcher contains CRLF line endings; the drive script would fail on Unix")
	}
	for _, must := range []string{"migration.key", "manifest.enc", "migrate-$os-$machine", "--dry-run", "--install-apps", "--destination \"$HOME\""} {
		if !strings.Contains(script, must) {
			t.Fatalf("launcher is missing %q\n%s", must, script)
		}
	}

	bat, err := os.ReadFile(filepath.Join(bundle, portableLauncherWin))
	if err != nil {
		t.Fatal(err)
	}
	batText := string(bat)
	if !strings.Contains(batText, "migrate-windows-amd64.exe") || !strings.Contains(batText, "%USERPROFILE%") {
		t.Fatalf("windows launcher is missing platform wiring:\n%s", batText)
	}

	keyBytes, err := os.ReadFile(filepath.Join(bundle, portableKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(keyBytes) != "012345678901234567890123456789012" {
		t.Fatalf("key was not copied intact: %q", keyBytes)
	}
}

func TestPackPortableDriveRefusesForeignKeyOverwrite(t *testing.T) {
	bundle := t.TempDir()
	if err := os.WriteFile(filepath.Join(bundle, manifestFile), []byte{0}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, formatFile), []byte(formatVersion), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, portableKeyFile), []byte(strings.Repeat("A", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "other.key")
	if err := os.WriteFile(other, []byte(strings.Repeat("B", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PackPortableDrive(bundle, other, ""); err == nil {
		t.Fatal("pack silently replaced a different drive key")
	}
}

func TestPackPortableDriveRequiresCapturedRepository(t *testing.T) {
	empty := t.TempDir()
	key := filepath.Join(t.TempDir(), "key")
	if _, err := PackPortableDrive(empty, key, ""); err == nil {
		t.Fatal("pack accepted a directory without a captured repository")
	}
}
