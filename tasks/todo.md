# Cross-Platform Migration Kit Task List

Tasks are ordered by dependency. Do not start implementation until the human
owner approves `SPEC.md`, `CAPABILITY-MAP.md`, and `tasks/plan.md`.

## Phase 0: Contract and Safety Foundation

- [x] Task 1: Define manifest/profile schemas, versioning, and exit codes
  - Acceptance: Valid profiles and manifests round-trip deterministically;
    unsupported versions fail closed.
  - Verify: Unit tests cover valid, malformed, unknown, and future-version input.
  - Files: `internal/migration/migration.go`, `internal/migration/migration_test.go`

- [x] Task 2: Define path, exclusion, symlink, and conflict invariants
  - Acceptance: All filesystem targets are rooted, unsafe links are rejected,
    and default conflicts are skip-and-report.
  - Verify: Adversarial path and symlink tests pass without modifying fixtures.
  - Files: `internal/migration/migration.go`, `internal/migration/migration_test.go`

- [x] Task 3: Add synthetic fixtures and threat-model tests
  - Acceptance: Fixtures include normal files, excluded secrets, symlinks,
    traversal attempts, duplicate names, and interrupted-copy conditions.
  - Verify: Tests assert the threat controls before the real bundle slice exists.
  - Files: `tests/fixtures/`, `tests/test_security.py`

- [x] Task 4: Define broad default capture and protected-path policy
  - Acceptance: Windows, macOS, and Linux define readable user roots,
    system/credential/cache exclusions, mounted-drive handling, and explicit
    decisions for unreadable or volatile paths.
  - Verify: Policy tests prove the default includes safe personal data without
    including OS roots, the repository itself, or credential stores.
  - Files: `internal/migration/migration.go`, `internal/migration/migration_test.go`

## Checkpoint: Contract

- [x] Manifest and profile contracts are stable and reviewed
- [x] Unsafe paths and profiles fail closed
- [x] No restore plan mutates destination data

## Phase 1: File Migration Vertical Slice

- [x] Task 5: Implement broad source inventory
  - Acceptance: Inventory discovers readable user data, sizes, metadata,
    exclusions, unreadable roots, and source platform without reading file
    contents into memory.
  - Verify: Temporary-tree integration tests cover large files, protected paths,
    mounted output roots, and exclusions.
  - Files: `internal/migration/migration.go`, `internal/migration/migration_test.go`

- [x] Task 6: Select and vendor open-source storage/encryption components
  - Acceptance: Selected components have OSI-approved, license-compatible terms;
    source versions and checksums are recorded; no external executable is
    needed at runtime.
  - Verify: Clean-machine tests run the binary with no Python, Go, Kopia, age,
    restic, package manager, cloud account, or network.
  - Files: `THIRD_PARTY_LICENSES.md`, `DEPENDENCIES.md`, `src/engine/`,
    `tests/test_standalone_runtime.*`

- [x] Task 7: Implement encrypted repository creation and metadata
  - Acceptance: Capture writes only encrypted repository content to the external
    drive and stores manifest, policy decisions, and checksums encrypted.
  - Verify: Integration tests create, inspect, restore, and verify a temporary
    repository; wrong credentials and tampering fail.
  - Files: `internal/migration/migration.go`, `internal/migration/migration_test.go`

- [x] Task 8: Implement inspect and dry-run restore planning
  - Acceptance: Destination mapping is deterministic and dry-run performs no
    file writes, deletes, package installs, or settings changes.
  - Verify: Snapshot destination before and after dry-run; assert identical state.
  - Files: `internal/migration/migration.go`, `cmd/migrate/main.go`,
    `internal/migration/migration_test.go`

- [x] Task 9: Implement restore, resume, and verification
  - Acceptance: Copy is resumable by item/checksum, conflicts follow policy,
    and verify reports missing or modified files with non-zero failure status.
  - Verify: End-to-end synthetic capture/restore/interruption/corruption test.
  - Files: `internal/migration/migration.go`, `internal/migration/state.go`,
    `internal/migration/migration_test.go`

## Checkpoint: File Migration

- [x] Capture to verify works on the development platform
- [x] Corruption and interruption are detected safely
- [x] Source files remain unchanged

## Phase 2: Platform Application Adapters

- [x] Task 10: Add Windows winget inventory adapter
  - Acceptance: Adapter exports package intent through argument arrays and never
    executes during capture or dry-run.
  - Verify: Fake winget executable tests cover success, warning, and absence.
  - Files: `internal/migration/apps.go`, `internal/migration/apps_test.go`

- [x] Task 11: Add macOS Homebrew Bundle adapter
  - Acceptance: Adapter captures a Brewfile and normalizes supported package
    intents without treating binaries as portable files.
  - Verify: Fake brew executable tests cover success, warning, and absence.
  - Files: `internal/migration/apps.go`, `internal/migration/apps_portable_test.go`

- [x] Task 12: Add Linux package inventory and install adapters
  - Acceptance: Detected `apt`, `dnf`, `pacman`, `zypper`, `apk`, `flatpak`, and
    `snap` managers produce normalized package intents and never run during
    capture or dry-run. The core remains usable without any of them.
  - Verify: Fake executable tests cover detection, normalization, unavailable
    packages, privilege requirements, and partial failures.
  - Files: `internal/migration/apps.go`, `internal/migration/apps_portable_test.go`

- [x] Task 13: Add explicit application install planning
  - Acceptance: Install commands are shown before execution, require an opt-in
    flag, and unsupported intents are reported without blocking file restore.
  - Verify: Tests prove no install occurs in dry-run and partial failure resumes.
  - Files: `internal/migration/apps.go`, `internal/migration/migration.go`,
    `internal/migration/apps_test.go`

## Phase 3: Portable Configuration

- [x] Task 14: Add reviewed portable configuration recipes
  - Acceptance: Git and shell/editor settings can be mapped with destination
    platform templates; private keys and tokens remain excluded.
  - Verify: Recipe tests cover path translation and secret exclusion.
  - Files: `internal/migration/settings.go`, `internal/migration/settings_test.go`

## Phase 4: UX and Release Quality

- [x] Task 15: Add redacted human-readable and JSON reports
  - Acceptance: Reports list moved, installed, skipped, conflicted, failed, and
    manual-action items without leaking secrets.
  - Verify: Golden report tests and secret-pattern assertions pass.
  - Files: `internal/migration/report.go`, `internal/migration/migration_test.go`

- [x] Task 16: Add doctor command, dependency checks, and actionable exit codes
  - Acceptance: Doctor explains unmet prerequisites and suggests safe next steps.
  - Verify: CLI tests cover missing tools, incompatible bundle, and incomplete
    restore states.
  - Files: `internal/migration/doctor.go`, `cmd/migrate/main.go`

- [x] Task 17: Add CI contract checks and release documentation
  - Acceptance: Windows, macOS, and Linux checks run in CI and release docs
    explain standalone binaries, bundled licenses, encryption credentials,
    offline operation, limitations, and recovery.
  - Verify: Full test/build/help commands pass in a clean environment.
  - Files: `.github/workflows/`, `docs/`, `go.mod`

- [x] Task 18: Add containerized Linux portability test
  - Acceptance: A Docker test image builds the standalone binary, runs the Go
    test suite, performs encrypted capture/verify/dry-run/restore, and checks
    the restored content without external tools at runtime.
  - Verify: `docker build --target test -t migration-kit-test .` followed by
    `docker run --rm migration-kit-test`; build the `scratch` release target.
  - Files: `Dockerfile`, `docker/smoke.sh`, `README.md`

## Approval Gate

- [ ] Human owner approves capability boundaries, encryption default, standalone
  runtime policy, profile policy, and package-install behavior

## Hardening 2026-10 (review round)

All fixed and covered by tests or validated end to end:

- [x] `migrate verify --destination` exits non-zero when any file is missing,
  unreadable, or checksum-mismatched (spec success criterion 7); the human
  report prints before the failure.
- [x] Managed restore applies captured permission bits (`os.Chmod`), matching
  what `RestoreWithOptions` promised; validated 0755/0600 round-trip in the
  Linux container.
- [x] Capture is resumable: re-running into an existing repository reuses
  verified objects and re-encrypts absent or tampered ones instead of failing
  with `O_EXCL` errors.
- [x] `doctor` exits non-zero when bundle, key, or manifest are unusable;
  destination findings stay advisory.
- [x] Failed restores print the partial report with a `migrate resume` hint.
- [x] Per-application install results are reported individually; a failure no
  longer marks every installable app as failed.
- [x] Protected-path policy implemented (spec "Always Exclude"): temp dirs,
  caches, crash dumps, browser profiles, `NTUSER.DAT`, registry hives,
  package/module caches, OS-managed containers, Trash, repository, symlinks.
- [x] macOS Brewfile `tap` lines are ignored instead of failing the whole
  inventory; partial inventories are kept with a recorded exclusion reason.
- [x] Linux inventory merges every detected package manager (was: first only).
- [x] Application equivalence mapping covers Windows, macOS, and Linux
  destinations with install-time native manager resolution; destination
  capability is checked during planning so dry-run stays honest.
- [x] `docker/smoke.sh` converted to LF and `.gitattributes` added; the
  documented Docker smoke test passes from a Windows checkout, and the
  `scratch` release image runs with zero dependencies.

### Portable drive + settings round (owner request: "one exe on the SSD, click, done")

All fixed and validated end to end this round:

- [x] `capture --portable` and `migrate pack` build a plug-and-play drive:
  launchers (`install.command`/`install.sh`/`install.bat`), the drive key, the
  destination binaries, and PORTABLE-README.txt. Launchers route to the
  platform binary at runtime (`uname`-based POSIX pick, `.exe` on Windows),
  run doctor → inspect → dry-run, ask yes/no, then restore with
  `--install-apps` and verify. Decline exits 0 without touching anything.
- [x] Six cross-compiled release binaries built from the documented commands
  (darwin/arm64+amd64, linux/arm64+amd64, windows/arm64+amd64); magic bytes
  verified per platform (the first build round was silently all-Windows
  because PowerShell set `$GOOS` as a scope variable, not an env var).
- [x] Settings recipes: git-v1 extended (aliases, init.defaultBranch,
  core.autocrlf with windows↔unix transform, credential helpers and
  subsections never captured), ssh-config-v1 (`~/.ssh/config` text only, keys
  stay excluded), vscode-v1 (settings.json scrubbed two-layer: secret-shaped
  key names AND credential-shaped values incl. `ghp_`, `sk-`, `xox`, `AKIA`,
  JWT, `Bearer` — found by E2E round 2 where `auth.gh.pat: ghp_…` slipped a
  name-only scrubber), plus extension capture/install through the `code` CLI.
- [x] BOM hardening: Windows-authored UTF-8-BOM `.gitconfig`/`settings.json`
  parse correctly (E2E round 1 produced an empty identity and an empty
  restored VS Code file).
- [x] Settings restore moved before application installs; a real apk failure
  (string `-y` unsupported by apk 3, E2E round 1) can no longer leave the
  destination without settings.
- [x] Per-manager Linux non-interactive flags: apt/dnf `-y`, pacman
  `--noconfirm`, zypper `--non-interactive`, apk `add --no-interactive`
  (verified against Alpine's apk 3.0).
- [x] Windows launcher hardening: `%~dp0` trailing backslash stripped before
  quoted arguments (it escaped the closing quote); decline path exits 0;
  doctor/inspect/dry-run gate each step.
- [x] `MIGRATE_USER_CONFIG_DIR` override for VS Code paths (safe testing on
  the dev machine; validated in the container where the real /root/.config
  receives the file).
- [x] Full E2E matrix re-run green after every fix: Windows native
  (keygen→capture→doctor→verify→dry-run→restore→verify), Windows one-click
  .bat (decline), Docker Alpine one-click install.sh (decline + full apply:
  16 real apk installs, .gitconfig identity/alias/autocrlf, .ssh/config
  without key material, scrubbed VS Code settings, verify pass), Docker test
  target (`go test ./...` green on Linux and Windows).

### Signed-in landing round (owner request: "OpenCode + Chrome land logged in")

All fixed and validated end to end this round:

- [x] `chrome-v1` recipe: bookmarks + installed extension IDs of the Default
  profile, restored at the platform-correct Chrome user-data location
  (AppData/Local on Windows, Library/Application Support on macOS, .config on
  Linux). Chrome passwords are NOT copyable by design (DPAPI/app-bound
  per-machine encryption) — the honest path is recorded in the checklist:
  sign in once, Google Sync restores passwords/sessions/extensions.
- [x] `opencode-v1` recipe: global config scrubbed with the two-layer
  secret scrubber; provider credentials (`~/.local/share/opencode/auth.json`)
  excluded by default, moved only with the new `capture --credentials`
  owner opt-in (restored 0600 under .local/share/opencode).
- [x] Inventory credential gate extended: any `auth.json`-shaped file is
  credential-like and excluded by default; with `--credentials` only
  `auth.json` under an `opencode` directory is exempted. SSH keys, keychains,
  and browser password stores stay excluded in every mode (unit-tested both
  modes).
- [x] `FINISH-SETUP.txt` checklist: capture collects one-time human steps
  (Chrome Sync sign-in, opencode re-auth or confirmed credentials), prints
  them in the capture summary, restores them as a numbered file at the
  destination, and reports each entry (skip-existing respected).
- [x] E2E rerun green: Windows native incl. chrome/opencode fixtures +
  `--credentials` (bookmarks restored, config scrubbed-clean, auth.json
  landed, checklist file verified, verify exit 0); Docker Alpine one-click
  accept (ROUND4-OK) and decline (ROUND5-OK — nothing written, exit 0);
  six dist binaries rebuilt from final source, magic bytes verified.

### General coverage round (owner request: "docker etc., terminal, coding languages — cover all")

All fixed and validated end to end this round:

- [x] Catalog-driven recipes (internal/migration/devenv.go): terminal-v1
  (shell profiles .bashrc/.profile/.bash_aliases/.zshrc/.zprofile/.tmux.conf,
  Windows Terminal settings.json packaged+unpackaged, PowerShell profiles
  Windows Documents + unix .config/powershell, alacritty, kitty), dev-v1
  (.npmrc, .yarnrc, pip conf/ini, cargo config.toml, go env, gradle props,
  maven settings.xml, version markers .tool-versions/.python-version/
  .node-version/.nvmrc/.ruby-version), docker-v1 (.docker/config.json with
  registry auths removed; Docker Desktop settings Windows/macOS), and
  kubernetes-v1 (.kube/config with token/cert-data/password/auth-provider
  dropped, clusters/contexts/CA-data kept).
- [x] Line scrubber with whole-word variable matching: drops credential
  shapes (ghp_/sk-/JWT/Bearer/xox/AKIA), userinfo URLs
  (https://user:pass@…), and secret-named assignments/export while whole-word
  matching keeps benign names (E2E caught: "export PATH" would have died on
  the "pat" substring marker; "certificate-authority-data" on "auth").
- [x] Inventory policy closed so scrubbed recipe copies are the ONLY copy:
  .docker/.kube/.password-store prefixes, root-exact shell/tool dotfiles,
  exact terminal/PowerShell/goenv/pip/cargo paths are inventory-excluded;
  project-local files of the same name still travel with the project.
- [x] App mappings completed: winget/brew/linux equivalents for kubectl
  (all three platforms) and docker for Linux hosts (Docker Desktop winget +
  brew-cask already existed).
- [x] Restore re-scrubs recipe content at restore time (an old bundle cannot
  re-emit credential material); FINISH-SETUP.txt now also carries
  docker login / npm login / pip re-auth / cloud kubectl sign-in lines.
- [x] E2E validated: Windows full matrix (9 recipes, raw inventory carries
  only opencode/vscode/plan example files — everything else via recipes;
  destination assertions: bashrc keeps alias+PATH and drops GITHUB_TOKEN,
  npmrc keeps registry and drops _authToken, docker config keeps credsStore
  and drops auths, kubeconfig keeps server/contexts and drops token, WT
  settings + PS profile + pip + cargo + goenv + tool-versions restored,
  checklist includes npm/pip/docker/kube re-logins); Docker Alpine one-click
  accept (ROUND6-OK, raw inventory carries ONLY Documents/plan.txt); the
  Docker test run also caught and fixed a stale dist binary (rebuilt before
  re-run).
- [x] All six dist binaries rebuilt from final source; magic bytes verified
  per platform; `go test ./...` green, coverage 69.2% statements.

## Physical Validation Still Required

- [x] Deterministic cross-platform adapter and repository end-to-end tests using
  fake package tools and temporary encrypted repositories.
- [x] Linux one-click launcher flow validated in a real Alpine container
  (decline + accept legs, real apk installs, settings recipes, scrub checks).
- [x] Add and publish the authoritative macOS test runbook for human and agent
  execution.
- [ ] Windows-to-Windows Hyper-V guest test with `--install-apps` in a disposable
  guest. Blocked in the current session because Hyper-V management is unavailable
  and enabling the feature requires an elevated Administrator session.
- [ ] One-click `install.command` on a real Mac (Gatekeeper right-click open,
  brew installs) with a real cross-arch darwin binary. Blocked until a
  physical Mac is available; the launcher logic is shared with the validated
  POSIX path, but darwin execution and Gatekeeper behavior need hardware.
- [ ] Windows-to-macOS test on a real Mac with Homebrew and curated mappings.
  Blocked until a physical Mac is available; Hyper-V cannot emulate macOS.
- [ ] Linux native package installation test on a disposable Linux host or VM.
