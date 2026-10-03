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

## Physical Validation Still Required

- [x] Deterministic cross-platform adapter and repository end-to-end tests using
  fake package tools and temporary encrypted repositories.
- [x] Add and publish the authoritative macOS test runbook for human and agent
  execution.
- [ ] Windows-to-Windows Hyper-V guest test with `--install-apps` in a disposable
  guest. Blocked in the current session because Hyper-V management is unavailable
  and enabling the feature requires an elevated Administrator session.
- [ ] Windows-to-macOS test on a real Mac with Homebrew and curated mappings.
  Blocked until a physical Mac is available; Hyper-V cannot emulate macOS.
- [ ] Linux native package installation test on a disposable Linux host or VM.
