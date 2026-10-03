# Spec: Cross-Platform Migration Kit

**Status:** Proposed for review. No implementation should proceed beyond the
documentation foundation until this specification and `tasks/plan.md` are
approved.

## Objective

Build a local-first CLI that helps a user move from a source computer to a new
destination computer through an external drive. The source may be Windows,
macOS, or Linux; the destination may be the same or a different supported
platform. The tool captures a manifest and selected personal data, creates an
encrypted and integrity-checked bundle, installs applications through native
package managers where possible, restores portable configuration through
allowlisted recipes, and produces a clear verification report.

### User Story

> As a person buying a new laptop, I want one command to capture my selected
> files and setup to an external drive, then one reviewed command on the new
> laptop to reproduce my working environment without manually repeating every
> step.

### Example Acceptance Scenario

The current Windows laptop is the source and a new Mac is the destination.
Capture produces an encrypted bundle containing selected user folders, a
portable application intent list, and portable settings. Restore installs
available Mac equivalents, copies files into the destination home directory
without unsafe overwrites, applies approved configuration recipes, and reports
Windows-only applications, unsupported settings, conflicts, and secrets that
were intentionally not migrated.

## Non-Goals

- Do not clone Windows or macOS system volumes.
- Do not copy drivers, kernel extensions, hardware identifiers, OS activation,
  arbitrary registry hives, launch databases, or undocumented system state.
- Do not promise that a Windows binary runs on macOS or vice versa.
- Do not bypass application licensing, activation, DRM, or vendor terms.
- Do not extract passwords, tokens, browser cookies, private keys, or OS
  credential-store contents by default.
- Do not delete or factory-reset the source machine.
- Do not overwrite destination data without an explicit conflict policy.
- Do not require a cloud account, server, or network connection for the core
  capture and restore flow.
- Do not require any separately installed runtime, encryption program, package
  manager, CLI, or vendor service for the core capture, inspect, restore,
  resume, or verify commands.

## Open-Source and Independence Policy

The following are hard requirements for the core product:

- The migration tool source code is open source under an OSI-approved license.
- All runtime code shipped in the standalone executable is open source with
  compatible licenses recorded in `THIRD_PARTY_LICENSES.md`.
- Release binaries are statically linked or otherwise self-contained for each
  supported operating system and architecture.
- Core operations work with no network, account, server, telemetry, or vendor
  API. An external drive and the destination operating system are the only
  required environment capabilities.
- The executable does not download code, plugins, package metadata, mappings,
  or updates at runtime. Updates are explicit user-installed releases.
- Any package-manager or application installer integration is an optional
  adapter. The core remains useful when no package manager, network, or
  installer is available.
- Dependency source, version, checksum, license, build instructions, and
  reproducibility status are recorded in the repository.

"Open source" applies to the migration tool and bundled runtime components. It
does not change the license of proprietary applications a user may choose to
install, and the tool will not redistribute those applications without a legal
right to do so.

## Supported Scope

### Data Classes

| Class | Default | Behavior |
|---|---|---|
| User documents, photos, downloads, and selected folders | Included by profile | Copy with metadata where supported; checksum each file |
| Portable shell/editor/Git configuration | Opt-in profile | Transform paths and line endings through recipes |
| Application inventory and package identifiers | Included | Reinstall through native package adapters |
| Application data | Per-app allowlist | Copy only documented, portable data paths |
| Browser profiles and cookies | Excluded | Require explicit vendor-supported export/import, future work |
| Passwords, tokens, certificates, private keys | Excluded | Use a dedicated password manager or manual vendor export |
| OS settings and system policy | Limited | Capture portable preferences only; report the rest |
| System files, drivers, and binaries | Excluded | Never include in a cross-platform bundle |

### Initial Platforms

- Windows 10/11 source and destination
- macOS 13+ source and destination, including Apple Silicon where possible
- Linux distributions with at least one supported package manager: `apt`,
  `dnf`, `pacman`, `zypper`, `apk`, `flatpak`, or `snap` where present

Linux is a first-class target for the core file/config flow and application
inventory. Distribution-specific installation support is best effort because
package names, repositories, privileges, and desktop environments differ.

### Initial Application Adapters

- Windows Package Manager (`winget`) export/import
- Homebrew Bundle on macOS (`Brewfile`)
- Linux native package inventory/install for detected `apt`, `dnf`, `pacman`,
  `zypper`, and `apk`
- Linux universal package inventory/install for detected `flatpak` and `snap`
- A generic manifest adapter for applications that have no package-manager match
- Curated cross-platform equivalence mappings for common browsers, editors,
  terminals, and developer tools
- Later: Microsoft Store limitations, Mac App Store identifiers, and selected
  developer package managers

The installer is "best effort" rather than "run anything found." It may install
only identifiers from a validated adapter manifest, never arbitrary commands
or scripts discovered in application data.

## Default Capture Scope

The default profile is intentionally broad. It discovers all readable files and
directories owned by the active user, including user home folders and
user-selected mounted data roots, then applies the protected exclusions below.
The profile records every root as `included`, `excluded`, `unreadable`,
`volatile`, or `unsupported`.

### Always Exclude

- Operating-system roots and system-managed directories.
- The source external-drive repository itself and other mounted backup targets.
- Virtual memory, hibernation, crash dumps, package caches, browser caches, and
  other reproducible volatile data.
- Live database files and application lock files unless a reviewed adapter first
  creates a consistent export.
- Password stores, browser cookies, session databases, access tokens, private
  keys, certificates, and credential-manager databases.
- Device nodes, sockets, reparse points/symlinks escaping an allowed root, and
  files that cannot be read safely.

"All folders by default" therefore means all safe, readable personal data, not
all bytes on the disk. Copying system internals would make cross-platform
restore less safe and would not produce a usable Mac/Linux installation.

## Commands

Commands are explicit about source, destination, profile, and bundle. No command
may infer a destructive destination from the current working directory.

```text
migrate capture --output <bundle-dir> --profile <profile> [--source <path>]
migrate inspect --bundle <bundle-dir> --profile <profile>
migrate restore --bundle <bundle-dir> --profile <profile> --dry-run
migrate restore --bundle <bundle-dir> --profile <profile> [--conflicts <policy>]
migrate resume --bundle <bundle-dir> --profile <profile>
migrate verify --bundle <bundle-dir> --profile <profile>
migrate doctor --bundle <bundle-dir> --profile <profile>
```

### Safety Rules

- `capture` refuses an output path inside any captured source path.
- `restore` requires `--dry-run` to have completed for the same bundle,
  destination identity, and profile before execution.
- The default conflict policy is `skip-and-report`; `overwrite` is never the
  default and requires an explicit flag plus a per-path allowlist.
- Restore is resumable by item identity and checksum, not by blindly repeating
  commands.
- Package install commands are generated by trusted adapters and never built by
  concatenating untrusted manifest text into a shell command.

## Technical Direction

- The implementation language is not a product constraint, but the first
  release should produce a self-contained cross-platform executable. Go is the
  leading candidate because it supports static builds and mature open-source
  filesystem/process tooling.
- Use proven open-source cryptographic and storage designs as references, but
  do not require Kopia, restic, age, or any other external executable at
  runtime. Selected components must be linked or vendored into the binary, or
  a narrowly scoped repository implementation must be written and audited.
- The CLI must fail closed if its bundled authenticated-encryption capability is
  unavailable or cannot pass its self-test.
- The migration manifest, inventory, plan, and restore state must be encrypted
  before writing to the external drive.
- Versioned JSON manifest with a schema version and compatibility rules.
- Adapter interface isolates OS-specific inventory, install, export, and verify
  behavior from the domain and bundle layers.
- Structured JSON logs and a human-readable report. Logs must not include file
  contents, secrets, or full personal paths unless explicitly requested locally.
- No network access is required by the core. Optional package adapters may use
  a package manager only after explicit restore approval, and should support a
  local/offline installer path where the platform permits it.

## Proposed Project Structure

```text
cmd/migrate/main.go
internal/migration/migration.go
internal/migration/apps.go
internal/migration/settings.go
internal/migration/state.go
internal/migration/report.go
internal/migration/doctor.go
internal/migration/*_test.go
docker/
.github/workflows/
docs/
tasks/
```

## Code Style

Prefer small pure functions at boundaries and explicit result types over hidden
global state. Platform code must be behind an adapter.

```go
def plan_copy(item: ManifestItem, destination: Path, policy: ConflictPolicy) -> CopyPlan:
    target := filepath.Join(destination, filepath.FromSlash(item.RelativePath))
    if not is_within_root(target, destination):
        return CopyPlan{}, errors.New("destination escapes restore root")
    if exists(target) && !overwrite {
        return CopyPlan{Item: item, Target: target, Action: "skip-existing"}, nil
    }
    return CopyPlan{Item: item, Target: target, Action: "copy"}, nil
```

Conventions:

- Go names follow standard `camelCase`/`PascalCase` conventions and commands
  use descriptive names.
- All paths are `filepath` values internally.
- All external input is schema-validated before domain logic sees it.
- No adapter may mutate state during inventory or dry-run.
- Errors are categorized, actionable, and safe to show to the user.

## Testing Strategy

- Unit tests for manifest validation, path mapping, exclusion rules, checksums,
  conflict policies, state transitions, and adapter command construction.
- Integration tests using temporary directories and fake package-manager
  executables. Tests must never run real package installs.
- Cross-platform contract tests run on Windows and macOS CI for path and adapter
  behavior where the platform is available.
- End-to-end fixture test: capture synthetic source tree, inspect, dry-run,
  restore into an empty destination, resume, and verify checksums/report.
- Security tests cover path traversal, symlink escapes, output-inside-source,
  malformed manifests, archive bombs, secret exclusion, and command injection.

The initial quality bar is full test pass, no unbounded filesystem traversal,
and no unencrypted personal-data bundle accepted by the default command.

The repository has native Go tests, a Linux Docker smoke test, and a
Windows/macOS/Linux CI matrix. Final host-specific application installation
still requires a disposable Hyper-V Windows guest and a physical Mac.

## Boundaries

- **Always:** validate paths and manifest input, use allowlists, encrypt by
  default, checksum transferred data, dry-run before restore, preserve source
  files, report every skip/conflict, keep secrets out of logs and Git, and run
  tests before commits.
- **Ask first:** adding a new secret or credential adapter, changing default
  include paths, adding a dependency, allowing overwrite/delete behavior,
  executing package-manager commands, or supporting a new platform.
- **Never:** commit personal bundles or keys, log secrets, run arbitrary manifest
  commands, follow symlinks outside an allowed root, silently overwrite files,
  or claim that an unsupported setting was restored.

## Success Criteria

1. A clean Windows, macOS, and Linux machine can run the standalone executable
   with no separately installed runtime or service.
2. `capture` discovers broad readable user data, creates a versioned encrypted
   repository on the external drive, and records manifest, exclusions, and
   source inventory in one command.
3. A destination can run `inspect` and `restore --dry-run` without modifying
   destination data or installing packages.
4. Restore copies selected files with deterministic conflict handling and can
   resume after interruption without duplicating or corrupting data.
5. Windows-to-macOS testing restores data/settings offline and installs as many
   compatible applications as local open-source or user-supplied installers can
   support; unavailable applications are reported without failing the whole
   migration.
6. Portable settings are transformed through reviewed recipes; unsupported or
   OS-specific settings are listed in the final report.
7. `verify` detects missing, changed, or checksum-mismatched files and returns a
   non-zero exit code when verification fails.
8. Tests demonstrate that secrets, excluded paths, path traversal, symlink
   escapes, and shell injection attempts are blocked.
9. A clean-machine integration test proves that core commands do not call the
   network, a cloud service, an installed third-party CLI, or a vendor API.
10. A user can understand exactly what moved, what was installed, what was
   skipped, and what manual action remains.

## Threat Model

### Assets

- Personal documents, source code, photos, and configuration
- Application inventory and usage patterns
- Encryption key or passphrase
- Integrity and correctness of the restored environment

### Trust Boundaries and Controls

| Boundary | Abuse case | Required control |
|---|---|---|
| Source filesystem | A symlink or reparse point escapes the selected root | Resolve and validate every entry; do not follow unsafe links |
| External drive | Bundle is tampered with or swapped | Authenticated encryption, manifest signature/checksum, bundle identity |
| Manifest/profile | Malicious path or command injection | Strict schema, allowlisted fields, no shell interpolation |
| Destination filesystem | Existing data is overwritten or deleted | Dry-run, explicit conflict policy, root/depth/ownership checks |
| Package manager | An app identifier resolves to an unexpected package | Adapter allowlist, show planned identifiers, require approval |
| Logs/reports | Personal data or secrets leak | Redaction, path minimization, local-only logs by default |
| Secrets adapter | Credential export is mishandled | Opt-in vendor-supported adapter, separate key, no generic scraping |

## Open Questions

- Which open-source storage/encryption implementation will be linked into the
  standalone binary, and which license-compatible components will be credited?
- Which system-managed directories need platform-specific exclusion rules in
  addition to the common protected-path policy?
- Should application equivalence be manual mapping only in v1, or may a curated
  community mapping database be added later?
- Which Linux package managers should be enabled by default when multiple are
  installed, and how should privilege escalation be requested?
- Should developer builds support package-manager installation while release
  users receive standalone binaries?

## Approval Gate

The human owner must approve this specification, `CAPABILITY-MAP.md`, and
`tasks/plan.md` before implementation begins. Any change to encryption defaults,
secret handling, overwrite/delete behavior, supported platforms, or package
execution requires a spec update first.
