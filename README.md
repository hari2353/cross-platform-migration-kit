# Cross-Platform Migration Kit

Cross-Platform Migration Kit is an open-source, local-first tool for moving a
person's files, selected settings, and application setup from one computer to
another using an external drive. It is designed for migrations such as
Windows to macOS, macOS to Windows, and same-platform upgrades.

## The Honest Promise

The kit aims to make a new laptop feel familiar with one source command and one
destination command. It does **not** clone an operating system or copy every
installed application byte-for-byte across different operating systems.

The portable migration unit is:

```text
source inventory
       |
encrypted repository on external drive
       |
destination preflight and dry-run
       |
native application installation + safe data/config restore
       |
verification report and unresolved-items list
```

## Existing Tools We Build Around

This project is an orchestrator and safety layer, not a replacement for every
platform tool:

- Apple Migration Assistant is the reference for same-platform Mac migration
  and officially transfers documents, apps, user accounts, and settings.
- Windows `winget export` and `winget import` provide a native application
  inventory and batch-install path on Windows.
- Homebrew Bundle provides a declarative `Brewfile` for macOS packages,
  casks, App Store apps, and selected developer tools.
- `chezmoi` demonstrates portable, templated, encrypted dotfile management
  across diverse machines.

The kit combines these ideas behind one manifest and reports what could not be
translated. It does not hide platform differences behind a false promise.
See [research and prior art](docs/research.md) for the source register.

## What the Default Profile Excludes

Capture walks every safe, readable file under the source root and applies a
fixed protected-path policy (specified in [`SPEC.md`](SPEC.md)); anything else
is copied. Permanently excluded by default, each with a recorded reason:

- Credential stores: `.ssh`, `.gnupg`, `.aws`, `.azure`, gcloud, keychains,
  Thunderbird, anything named like a private key, token, or credentials file.
- Browser profiles and caches (Chrome/Edge/Firefox user data) — migrating
  browser cookies or password databases is never safe or portable by file copy.
- Volatile and reproducible state: Temp directories, caches, crash dumps,
  `NTUSER.DAT`, page/hibernation files, `.DS_Store`, `Thumbs.db`.
- Package and module caches: npm, pip, NuGet, Go modules, Gradle, Maven, Cargo.
- OS-managed application containers: Windows `Packages` state, WindowsApps
  stubs, Trash/Recycle Bin.
- The migration repository itself, symbolic links and reparse points, and
  anything unreadable — recorded as exclusions with reasons, never silently
  skipped.

Every exclusion is written to the manifest and appears in the inspect and
doctor reports, so nothing disappears without a trace.

## Application Installation Coverage

`--apps` captures installed-application intents with the native package
manager of the source: winget on Windows, Homebrew Bundle on macOS, and
apt/dnf/pacman/zypper/apk plus Flatpak and Snap on Linux (all detected
managers are merged). On restore, apps install through the destination's own
native manager. A curated mapping table covers common browsers, editors,
terminals, media, messaging, and developer tools (Chrome/Brave/Firefox, VS
Code, Obsidian, Notion, Discord, Slack, Telegram, Zoom, Spotify, Steam,
Postman, OBS, VLC, 7-Zip, Docker Desktop, JetBrains Toolbox, git/gh/node/
python/go/rustup/powershell and more) across Windows, macOS, and Linux
targets; anything without a reviewed equivalent is reported as `unsupported`
and shown in the final report instead of being force-installed. Intent lists
are only lookup keys — install commands themselves are ever composed from the
curated, reviewed values, never from manifest text. Each manager gets its own
non-interactive flag (`apt/dnf install -y`, `pacman --noconfirm`, `zypper
--non-interactive`, `apk add --no-interactive`), verified against current
package-manager CLIs.

## Portable Settings Recipes

Capture records nine reviewed recipes and restores them onto the
destination, each reported independently:

- **git-v1** — identity (`user.name`, `user.email`), `init.defaultBranch`,
  `[alias]` shortcuts, and `core.autocrlf` with a platform transform:
  Windows checkout normalization (`true`) becomes `input` on macOS/Linux, and
  `input` becomes `true` for Windows destinations. Credential helpers and
  section subsections are never captured.
- **ssh-config-v1** — the text of `~/.ssh/config` only. Private keys, public
  keys, and `known_hosts` stay excluded by the protected-path policy; only
  host aliases, users, and connection options travel.
- **vscode-v1** — VS Code `settings.json` (default location per platform)
  scrubbed at capture time: any key whose name looks secret-shaped
  (token/secret/password/credential/auth/…) **and** any value matching a
  real credential shape (`ghp_…`, `github_pat_…`, `sk-…`, Slack `xox…`,
  AWS `AKIA…`, `Bearer …`, JWT `eyJ…`) is dropped and recorded as an
  exclusion. When the `code` CLI is available, the extension list is also
  captured and reinstalled on the destination with the
  `--install-apps` permission.
- **chrome-v1** — Chrome bookmarks and the installed extension list of the
  Default profile, placed at the platform-correct location on the
  destination. Bookmarks contain no secrets; password stores, cookies, and
  caches stay excluded. Browser passwords themselves are genuinely not
  copyable: Chrome encrypts them with per-machine keys (DPAPI on Windows,
  app-bound keys on modern Chrome), so a copied login file cannot decrypt on
  another computer. The supported path is written to the destination's
  `FINISH-SETUP.txt`: **sign in to your Google account once and Chrome Sync
  restores passwords, sessions, and extensions automatically.**
- **opencode-v1** — the OpenCode global config (`~/.config/opencode/
  opencode.json`), scrubbed with the same two-layer rules as VS Code
  settings. Provider credentials (`~/.local/share/opencode/auth.json`) are
  **excluded by default** both by the recipe and by the inventory's
  credential-like path gate; run `opencode auth login` on the destination
  (the checklist tells you). Owners who accept moving their own plain-text
  API keys through the always-encrypted bundle can pass `--credentials` to
  capture — the single, recorded, opt-in exception. Private keys, OS
  keychains, and browser password stores are never eligible for this flag.
- **terminal-v1** — shell profiles and terminal configuration, scrubbed line
  by line: `.bashrc`, `.profile`, `.bash_aliases`, `.zshrc`, `.zprofile`,
  `.tmux.conf`, Windows Terminal `settings.json`, PowerShell profiles
  (Windows `Documents\PowerShell` / `WindowsPowerShell`, macOS & Linux
  `~/.config/powershell`), and alacritty/kitty configs. Rebuilt aliases,
  options, keybindings, and prompts travel; any line that embeds a
  credential by shape (`ghp_…`, `sk-…`, JWT, `Bearer …`), exports a
  secret-named variable (`GITHUB_TOKEN`, `ANTHROPIC_API_KEY`), or hides a
  password inside a URL (`https://user:pass@host`) is dropped and listed as
  an exclusion. Benign variables are protected by whole-word matching —
  `PATH` never matches "pat"; `certificate-authority-data` never matches
  "auth".
- **dev-v1** — language-runtime configuration: `.npmrc`, `.yarnrc`, pip
  config (`pip.conf` / `pip.ini`), cargo `config.toml`, the go env file,
  gradle properties, maven `settings.xml`, and version markers
  (`.tool-versions`, `.python-version`, `.node-version`, `.nvmrc`,
  `.ruby-version`). Registry tokens, index passwords, and Maven credentials
  are scrubbed; the checklist names exactly which logins to redo.
- **docker-v1** — the Docker client config (`~/.docker/config.json`) with
  the registry `auths` block (base64 user:password) removed, while
  `credsStore`/`credHelpers` references and formatting preferences stay.
  Docker Desktop settings on Windows and macOS are carried through the
  generic JSON scrub. The checklist says to run `docker login`.
- **kubernetes-v1** — the kubeconfig (`~/.kube/config`) with credential
  entries dropped (`token`, `client-certificate-data`, `client-key-data`,
  `password`, `auth-provider`, …) while clusters, contexts, namespaces, and
  public `certificate-authority-data` stay intact. Cloud-exec auth plugins
  regenerate the removed material at first sign-in; the checklist names the
  `az` / `gcloud` / `aws` sign-in.

Every recipe-owned file is **also excluded from the raw file inventory**, so
the scrubbed copy is the only copy that can travel — a token embedded in a
shell profile can never bypass the scrubber through the file layer.
Project-local files with the same names (a monorepo's `.npmrc`, a project's
`.tool-versions`) still travel untouched with the project.

## Finish-Setup Checklist

Recipes leave one-time, human steps (account sign-ins, re-auth, re-logins).
They are collected at capture, shown in the capture summary, restored as
numbered entries in `FINISH-SETUP.txt` at the destination root, and printed
in the restore report — so the destination opens already personalized and
the few interactive steps are written down in one place. Examples: Chrome
Sync sign-in, `docker login`, `npm login`, cloud Kubernetes
(`az login` / `gcloud auth login` / `aws sso login`), pip index
re-authentication.

## One-Click Portable Drive

`capture --portable` turns the external drive into a plug-and-play bundle by
writing one launcher per platform next to the encrypted repository, plus
(prebuilt) destination executables and the drive key:

```text
E:\migration-repository\
  manifest.enc, format, objects\   the encrypted repository
  migration.key                    the drive key (see warning below)
  migrate-darwin-arm64 / -amd64    macOS executables
  migrate-linux-arm64  / -amd64    Linux executables
  migrate-windows-arm64.exe / -amd64.exe
  install.command / install.sh / install.bat
  PORTABLE-README.txt
```

On the new laptop: plug in the drive, open the folder, double-click the
launcher for that platform (`install.command` on macOS — right-click → Open
once if Gatekeeper asks). The launcher runs `doctor` → `inspect` → a full
`--dry-run` plan, asks **yes/no**, and only on consent runs
`restore --install-apps` and then `verify` — one click from capture to a
familiar machine. The ASCII flow is platform-routed at runtime: the POSIX
launcher picks `migrate-<uname>-<arch>` from the drive, the Windows
launcher runs the bundled `.exe`.

Prebuilt destination binaries come from a release directory (any binaries
missing there are skipped silently):

```powershell
$env:CGO_ENABLED = "0"
foreach ($t in "darwin/arm64","darwin/amd64","linux/amd64","linux/arm64","windows/amd64","windows/arm64") {
  $parts = $t.Split("/"); $env:GOOS = $parts[0]; $env:GOARCH = $parts[1]
  $out = "dist\migrate-$($parts[0])-$($parts[1])"; if ($parts[0] -eq "windows") { $out += ".exe" }
  go build -trimpath -ldflags "-s -w" -o $out .\cmd\migrate
}
migrate capture --source C:\Users\you --output E:\migration-repository --key-file E:\migration.key --apps --portable --binaries dist
# or later: migrate pack --bundle E:\migration-repository --key-file E:\migration.key --binaries dist
```

`MIGRATE_USER_CONFIG_DIR` overrides the VS Code settings root for tests and
sandboxed runs. The drive key tradeoff: storing `migration.key` beside the
bundle is what makes one-click restore possible; anyone holding the whole
drive holds both the repository and its key, so physically protect the drive
(or delete the key from the drive and type its path when you prefer).

## Standalone and Open Source

The migration tool itself will be fully open source and will run as a
self-contained, statically linked executable on Windows, macOS, and Linux. It
will not require an installed Python runtime, Go runtime, Kopia executable,
`age` executable, cloud account, hosted server, telemetry service, or network
connection for capture, inspect, restore, resume, or verify.

Kopia, restic, age, and similar projects are research and implementation
references, not mandatory runtime dependencies. If an open-source component is
used, the required code will be statically linked or included in the release,
with its license and source attribution documented. The external drive will
contain the tool's own encrypted repository format and metadata, not plaintext
files or a dependency on a third-party service.

Application installation is a separate, optional capability. The core can
restore files and portable settings fully offline. Installing third-party apps
may require an existing local installer or an operating-system package source;
the tool will never make that requirement part of core capture/restore or claim
that proprietary applications are open source.

### Review hardening (JS pass 2)

Fixes applied after the first full end-to-end review so the kit matches its
spec; see `tasks/todo.md` "hardening 2026-10" for the list:

- `verify --destination` now fails with exit code 1 when files are missing or
  tampered (spec success criterion 7).
- Restored files keep their captured permission bits; interrupted captures can
  safely re-run into an existing repository and repair tampered objects.
- macOS Brewfile tap lines no longer destroy the whole application inventory,
  and partial inventories are kept with a recorded warning.
- Linux captures merge intents from every detected package manager instead of
  the first one, and cross-platform app equivalence now covers Windows, macOS,
  and Linux destinations (winget/brew/apt/dnf/pacman/zypper/apk/flatpak/snap).

### Review hardening (round 3: portable drive + settings)

- Windows-authored UTF-8-BOM `.gitconfig`/`settings.json` files now parse
  correctly (the BOM corrupted the first `[user]` section and made VS Code
  JSON unreadable on real Windows setups).
- VS Code capture never stores an empty settings blob, and the scrubber is
  two-layer: secret-shaped key names *and* credential-shaped values
  (`ghp_…`, `sk-…`, `xox…`, `AKIA…`, JWTs, `Bearer …`) are dropped and
  recorded.
- Settings restore now runs **before** application installs, so a package
  manager failure (offline, unsupported store) can no longer block identity,
  SSH, or editor settings.
- Linux installs use per-manager non-interactive flags — the shared `-y`
  broke on apk 3 (`unrecognized option 'y'`) and was never valid for pacman
  or zypper; apk also uses the `add` verb and `--no-interactive`.
- The portable Windows launcher strips the `%~dp0` trailing backslash before
  passing quoted arguments (a trailing `\` escapes the closing quote in the
  Windows argument parser) and exits 0 when the user declines.
- Settings restore is covered on every host OS by tests; the Windows .bat
  launcher and the POSIX install.sh launcher were executed end to end
  (decline leaves the destination untouched, accept performs the full
  restore).

## Planned Commands


The command names are part of the initial contract and may be refined during
implementation:

```powershell
# On the source computer, with the external drive mounted:
migrate capture --source C:\Users\you --output E:\migration-repository --key-file E:\migration.key --profile personal --apps

# Optional: turn the drive into the one-click bundle (launchers + key + destination binaries):
migrate pack --bundle E:\migration-repository --key-file E:\migration.key --binaries .\dist

# On the destination computer, inspect what will happen:
migrate inspect --bundle E:\migration-repository --key-file E:\migration.key --profile personal
migrate restore --bundle E:\migration-repository --destination C:\Users\new-user --key-file E:\migration.key --profile personal --dry-run

# Execute after reviewing the plan:
migrate restore --bundle E:\migration-repository --destination C:\Users\new-user --key-file E:\migration.key --profile personal

# Re-run safely after fixing a skipped dependency or conflict:
migrate resume --bundle E:\migration-repository --key-file E:\migration.key --profile personal

# Check the final result:
migrate verify --bundle E:\migration-repository --destination C:\Users\new-user --key-file E:\migration.key
migrate doctor --bundle E:\migration-repository --destination C:\Users\new-user --key-file E:\migration.key
```

The first usable release will expose the same commands through the standalone
executable on all supported platforms.

Current local smoke-test commands:

```powershell
go test ./...
go build -trimpath -ldflags="-s -w" -o .\migrate.exe .\cmd\migrate

# Linux portability test in Docker
docker build --target test -t migration-kit-test .
docker run --rm migration-kit-test

# Build the dependency-free runtime image
docker build --target release -t migration-kit:local .
```

The current first slice uses the Go standard library only. See
[`DEPENDENCIES.md`](DEPENDENCIES.md) and
[`THIRD_PARTY_LICENSES.md`](THIRD_PARTY_LICENSES.md).

Docker is a Linux portability test, not a macOS or Windows emulator. It
validates the standalone binary in a clean Linux userland. Native Windows,
macOS, and Linux host tests are still required for path, permission, package,
and settings adapters.

## Current Status

This repository starts with a specification and design contract. The spec must
be reviewed before implementation. See:

1. [Capability map](CAPABILITY-MAP.md)
2. [Product specification](SPEC.md)
3. [Implementation plan](tasks/plan.md)
4. [Task checklist](tasks/todo.md)

### Test Runbooks

- [macOS test runbook](docs/MAC-TEST-RUNBOOK.md): authoritative instructions
  for a human or agent testing a real Mac.
- [Hyper-V test procedure](docs/testing-hyperv.md): Windows guest validation and
  the limits of Hyper-V for macOS testing.

## Security Warning

The repository may contain personal documents and application configuration.
Use the tool's authenticated encryption, keep the repository password/key
separate from the drive **or** consciously accept the one-click-drive tradeoff
described above, and verify the repository before deleting the source.
Never commit a real repository, key, credential export, or personal
configuration to GitHub.

## Development

The current implementation is Go 1.26 with only the standard library. The
standalone binary is built with `CGO_ENABLED=0` and does not require Go after
compilation.

## License

Apache-2.0. See [LICENSE](LICENSE).
