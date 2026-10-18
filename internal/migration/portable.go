package migration

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Portable bundle layout on the drive:
//
//   migration.key            the 32-byte secret key
//   manifest.enc + objects/  the encrypted repository
//   migrate-<os>-<arch>       static destination executables (any platform can pick its own)
//   install.command           macOS double-click launcher
//   install.sh                Linux launcher
//   install.bat               Windows launcher
//   PORTABLE-README.txt       short instructions for the user
//
// The destination flow: plug the drive in, run the launcher for that platform,
// review the plan, confirm, and the documents, settings recipes, and
// application installs happen with no other steps.

const (
	portableKeyFile      = "migration.key"
	portableReadmeFile   = "PORTABLE-README.txt"
	portableLauncherPOSX = "install.sh"
	portableLauncherMac  = "install.command"
	portableLauncherWin  = "install.bat"
)

// portableBinaryNames lists the cross-compiled binaries copied onto the drive
// when a binaries directory is provided. Every supported destination platform
// keeps its own static executable so one drive works for every future laptop.
var portableBinaryNames = []string{
	"migrate-darwin-arm64",
	"migrate-darwin-amd64",
	"migrate-linux-amd64",
	"migrate-linux-arm64",
	"migrate-windows-amd64.exe",
	"migrate-windows-arm64.exe",
}

// PackPortableDrive validates the bundle and writes the launcher scripts plus
// (optionally) the destination binaries and key onto the external drive.
func PackPortableDrive(bundlePath, keyPath, binariesDir string) ([]string, error) {
	written := []string{}
	manifest, err := os.Stat(filepath.Join(bundlePath, manifestFile))
	if err != nil || manifest.IsDir() {
		return nil, errors.New("portable bundle needs a captured repository: run capture before pack (manifest.enc missing)")
	}
	format, err := os.Stat(filepath.Join(bundlePath, formatFile))
	if err != nil || format.IsDir() {
		return nil, errors.New("repository format marker missing; the bundle is not a migration repository")
	}

	// The key travels on the drive next to the repository so the destination
	// can decrypt without the source machine. Guard against partial copies.
	if keyPath != "" {
		keyBytes, keyErr := os.ReadFile(keyPath)
		if keyErr != nil {
			return nil, fmt.Errorf("read key: %w", keyErr)
		}
		keyTarget := filepath.Join(bundlePath, portableKeyFile)
		existing, existsErr := os.ReadFile(keyTarget)
		switch {
		case existsErr == nil && string(existing) == string(keyBytes):
			// Already present and identical: reuse.
		case existsErr == nil:
			return nil, errors.New("a different migration.key already exists on the drive")
		default:
			if err := os.WriteFile(keyTarget, keyBytes, 0o600); err != nil {
				return nil, fmt.Errorf("write drive key: %w", err)
			}
			written = append(written, keyTarget)
		}
	}

	if binariesDir != "" {
		for _, name := range portableBinaryNames {
			source := filepath.Join(binariesDir, name)
			info, err := os.Stat(source)
			if err != nil || info.IsDir() {
				continue // a platform without a prebuilt binary stays absent silently
			}
			target := filepath.Join(bundlePath, name)
			if err := copyPortableFile(source, target, info.Size()); err != nil {
				return nil, err
			}
			if err := osChmod(target, 0o755); err != nil && !errors.Is(err, os.ErrPermission) {
				return nil, err
			}
			written = append(written, target)
		}
	}

	launcher := portablePOSIXLauncher()
	for _, name := range []string{portableLauncherPOSX, portableLauncherMac} {
		target := filepath.Join(bundlePath, name)
		if err := os.WriteFile(target, []byte(launcher), 0o755); err != nil {
			return nil, fmt.Errorf("write %s: %w", name, err)
		}
		written = append(written, target)
	}
	if err := os.WriteFile(filepath.Join(bundlePath, portableLauncherWin), []byte(portableWindowsLauncher()), 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", portableLauncherWin, err)
	}
	written = append(written, filepath.Join(bundlePath, portableLauncherWin))
	readme := filepath.Join(bundlePath, portableReadmeFile)
	if err := os.WriteFile(readme, []byte(portableReadmeText()), 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", portableReadmeFile, err)
	}
	written = append(written, readme)
	return written, nil
}

// copyPortableFile copies in small chunks so a corrupted source does not
// balloon memory, and removes a partial target on failure.
func copyPortableFile(source, target string, size int64) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	temporary := target + ".tmp"
	output, err := os.Create(temporary)
	if err != nil {
		return err
	}
	if _, err := input.WriteTo(output); err != nil {
		fileClose(output)
		os.Remove(temporary)
		return err
	}
	if err := fileSync(output); err != nil {
		fileClose(output)
		os.Remove(temporary)
		return err
	}
	if err := fileClose(output); err != nil {
		os.Remove(temporary)
		return err
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(temporary, target)
}

// portablePOSIXLauncher is the shared macOS (.command) and Linux (.sh) flow:
// find the repository and key next to the launcher, show the plan, ask, apply.
func portablePOSIXLauncher() string {
	return strings.Join([]string{
		`#!/bin/sh`,
		`# Cross-Platform Migration Kit - portable one-click installer.`,
		`# Documents, portable settings, and application installs for this machine.`,
		`set -u`,
		``,
		`DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd) || exit 1`,
		`KEY="$DIR/migration.key"`,
		`REPO=""`,
		`MIGRATE_BINARY=""`,
		``,
		`if [ ! -f "$KEY" ]; then`,
		`    echo "migrate: migration.key not found next to this script at $KEY" >&2`,
		`    echo "       copy the key file from the source computer first." >&2`,
		`    exit 1`,
		`fi`,
		``,
		`os="$(uname -s | tr 'ABCDEFGHIJKLMNOPQRSTUVWXYZ' 'abcdefghijklmnopqrstuvwxyz')"` + " # darwin/linux",
		`machine="$(uname -m)"`,
		`case "$machine" in`,
		`    x86_64) machine=amd64 ;;`,
		`    aarch64|arm64) machine=arm64 ;;`,
		`esac`,
		`for candidate in "$DIR/migrate-$os-$machine" "$DIR/migrate"; do`,
		`    if [ -f "$candidate" ]; then MIGRATE_BINARY="$candidate"; break; fi`,
		`done`,
		`if [ -z "$MIGRATE_BINARY" ]; then`,
		`    echo "migrate: no destination executable (migrate-$os-$machine) found on the drive." >&2`,
		`    echo "       ask the source computer to re-run capture with --portable after building the release binaries." >&2`,
		`    exit 1`,
		`fi`,
		`chmod +x "$MIGRATE_BINARY" 2>/dev/null || true`,
		``,
		`if [ -f "$DIR/manifest.enc" ]; then`,
		`    REPO="$DIR"`,
		`else`,
		`    for entry in "$DIR"/*/; do`,
		`        if [ -f "$entry/manifest.enc" ]; then REPO="${entry%/}"; break; fi`,
		`    done`,
		`fi`,
		`if [ -z "$REPO" ]; then`,
		`    echo "migrate: no repository (manifest.enc) found on this drive." >&2`,
		`    exit 1`,
		`fi`,
		``,
		`echo "=== health check ==="`,
		`"$MIGRATE_BINARY" doctor --bundle "$REPO" --key-file "$KEY" --destination "$HOME" || exit 1`,
		`echo "=== what will move ==="`,
		`"$MIGRATE_BINARY" inspect --bundle "$REPO" --key-file "$KEY" || exit 1`,
		`echo "=== restore plan (nothing is written yet) ==="`,
		`"$MIGRATE_BINARY" restore --bundle "$REPO" --destination "$HOME" --key-file "$KEY" --dry-run || exit 1`,
		``,
		`printf 'Apply now into %s? [yes/no]: ' "$HOME"`,
		`read -r REPLY || REPLY=""`,
		`case "$REPLY" in`,
		`    [Yy]|[Yy][Ee][Ss]) ;;`,
		`    *) echo "Nothing was changed on this machine."; exit 0 ;;`,
		`esac`,
		``,
		`echo "=== applying migration ==="`,
		`"$MIGRATE_BINARY" restore --bundle "$REPO" --destination "$HOME" --key-file "$KEY" --install-apps`,
		`status=$? || true`,
		`echo "=== verifying result ==="`,
		`"$MIGRATE_BINARY" verify --bundle "$REPO" --key-file "$KEY" --destination "$HOME"`,
		`exit $status`,
	}, "\n") + "\n"
}

func portableWindowsLauncher() string {
	return strings.Join([]string{
		`@echo off`,
		`setlocal`,
		`rem Cross-Platform Migration Kit - portable one-click installer.`,
		`set "DIR=%~dp0"`,
		`rem %~dp0 ends with a backslash; a trailing backslash would escape the`,
		`rem closing quote of every command-line argument below, so strip it.`,
		`set "DIR=%DIR:~0,-1%"`,
		`set "KEY=%DIR%\migration.key"`,
		`set "MIGRATE_BINARY=%DIR%\migrate-windows-amd64.exe"`,
		`set "REPO=%DIR%"`,
		`if not exist "%KEY%" (`,
		`    echo migrate: migration.key not found next to this script at "%KEY%"`,
		`    echo Copy the key file from the source computer to this folder first.`,
		`    goto stop`,
		`)`,
		`if not exist "%MIGRATE_BINARY%" (`,
		`    echo migrate: no destination executable found on this drive.`,
		`    goto stop`,
		`)`,
		`if not exist "%DIR%\manifest.enc" (`,
		`    set "REPO="`,
		`    for /d %%D in ("%DIR%\*") do if exist "%%D\manifest.enc" set "REPO=%%D"`,
		`)`,
		`if not defined REPO (`,
		`    echo migrate: no repository ^(manifest.enc^) found on this drive.`,
		`    goto stop`,
		`)`,
		`echo === health check ===`,
		`"%MIGRATE_BINARY%" doctor --bundle "%REPO%" --key-file "%KEY%" --destination "%USERPROFILE%"`,
		`if errorlevel 1 goto stop`,
		`echo === what will move ===`,
		`"%MIGRATE_BINARY%" inspect --bundle "%REPO%" --key-file "%KEY%"`,
		`if errorlevel 1 goto stop`,
		`echo === restore plan (nothing is written yet) ===`,
		`"%MIGRATE_BINARY%" restore --bundle "%REPO%" --destination "%USERPROFILE%" --key-file "%KEY%" --dry-run`,
		`if errorlevel 1 goto stop`,
		`set "REPLY="`,
		`set /p REPLY=Apply now into "%USERPROFILE%"? [yes/no]: `,
		`if /I not "%REPLY%"=="yes" if /I not "%REPLY%"=="y" (`,
		`    echo Nothing was changed on this machine.`,
		`    goto declined`,
		`)`,
		`echo === applying migration ===`,
		`"%MIGRATE_BINARY%" restore --bundle "%REPO%" --destination "%USERPROFILE%" --key-file "%KEY%" --install-apps`,
		`set "STATUS=%errorlevel%"`,
		`echo === verifying result ===`,
		`"%MIGRATE_BINARY%" verify --bundle "%REPO%" --key-file "%KEY%" --destination "%USERPROFILE%"`,
		`if not "%STATUS%"=="0" exit /b %STATUS%`,
		`goto done`,
		`:declined`,
		`echo.`,
		`pause`,
		`exit /b 0`,
		`:stop`,
		`echo.`,
		`pause`,
		`exit /b 1`,
		`:done`,
		`echo.`,
		`echo Migration complete. Review the report above.`,
		`pause`,
		`exit /b 0`,
	}, "\r\n") + "\r\n"
}

func portableReadmeText() string {
	return strings.Join([]string{
		"Cross-Platform Migration Kit - portable bundle",
		"===============================================",
		"",
		"This drive contains an encrypted copy of a person's files, portable",
		"settings (Git identity/aliases, SSH host configuration, VS Code settings)",
		"and application intents, plus one executable per destination platform.",
		"",
		"1. Plug this drive into the new laptop.",
		"2. Open this folder and double-click the launcher for that laptop:",
		"     macOS:   install.command   (if macOS refuses: right-click > Open once)",
		"     Linux:   install.sh        (run it from a terminal if needed)",
		"     Windows: install.bat",
		"3. Review the printed plan. Nothing is written until you type yes.",
		"",
		"NOTES",
		"-----",
		"- migration.key is stored on this drive so the destination can decrypt the",
		"  bundle. Keep this drive safe: it is readable only with this kit's",
		"  encryption, but anyone holding both the key and the drive can restore it.",
		"- Application installs use the destination platform's own package manager",
		"  and may require its approval for network access or privileges.",
		"- Browser logins, password managers, and OS-level settings are never moved",
		"  by file copy; the final report lists anything that needs manual care.",
		"",
	}, "\n")
}
