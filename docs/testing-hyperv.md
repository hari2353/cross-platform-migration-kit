# Hyper-V End-to-End Testing

Hyper-V is useful for validating the Windows-to-Windows workflow, but it cannot
emulate macOS. A Hyper-V Windows guest can prove:

- The standalone binary runs on a clean Windows destination.
- Files restore into a new user profile.
- A captured Windows `winget` application manifest is parsed and passed to
  `winget import`.
- Compatible applications install when the guest has network access and the
  package sources are available.
- Dry-run does not write files or install applications.
- Verification catches missing or changed restored files.

It cannot prove Windows-to-macOS application equivalence, macOS preferences,
Apple Silicon behavior, or Mac application licensing. Those require a real Mac
or a separately available Apple-supported test environment.

## Host Requirements

Microsoft documents these requirements for Hyper-V on Windows:

- Windows 10/11 Pro or Enterprise, not Home.
- A 64-bit processor with SLAT and hardware virtualization enabled.
- At least 4 GB RAM, with more required for a useful guest.
- An elevated Administrator PowerShell session to enable the feature.

Source: [Microsoft: Install Hyper-V in Windows](https://learn.microsoft.com/en-us/windows-server/virtualization/hyper-v/get-started/install-hyper-v)

The current development host was checked during validation. Hyper-V could not
be queried from the non-elevated session: `Get-VM` was unavailable and the
Windows optional-feature query returned an elevation-required error. No VM was
created, started, or modified by the test session.

Enable Hyper-V from **Administrator PowerShell**:

```powershell
Enable-WindowsOptionalFeature -Online -FeatureName Microsoft-Hyper-V -All
```

Windows will normally require a restart. Verify the feature and management
module after restarting:

```powershell
Get-WindowsOptionalFeature -Online -FeatureName Microsoft-Hyper-V-All
Get-Command Get-VM
```

## Test Matrix

The repository includes a deterministic end-to-end test that uses fake package
tools and temporary encrypted repositories. It validates the migration lifecycle
and cross-platform planning without installing software:

```powershell
go test ./internal/migration -run 'TestPortableApplicationAdaptersEndToEnd|TestRepositoryMigrationEndToEndWithResumeAndReports' -count=1
```

This is not a substitute for installation on a disposable destination. It
proves that the commands and compatibility decisions are deterministic and
that no package manager is invoked during planning.

### Test A: Windows Source to Windows Guest

1. Create a clean Windows 11 Pro/Enterprise guest and a disposable local test
   account. Do not use your production profile or secrets.
2. Copy the standalone `migrate.exe` and a separately protected test key into
   the guest. Do not copy the host's real key.
3. On the source Windows machine, capture a fixture containing synthetic data
   and `--apps` application intent.
4. Copy the encrypted repository and executable to the guest using a mounted
   test share or virtual disk.
5. Run `restore --dry-run` and verify that the destination remains unchanged.
6. Run `restore --install-apps` only in the disposable guest. This may use
   network access through `winget`; package agreement and installer prompts
   remain platform/vendor controlled.
7. Run `verify`, inspect installed application IDs, and compare a known set of
   application executables or package records.
8. Snapshot or discard the guest after the test. Do not treat package install
   success as proof that all applications are portable.

## Commands

The current CLI flags are:

```powershell
migrate.exe capture --source C:\MigrationFixture --output E:\MigrationRepo --key-file E:\migration.key --apps
migrate.exe inspect --bundle E:\MigrationRepo --key-file E:\migration.key
migrate.exe restore --bundle E:\MigrationRepo --destination C:\Users\MigrationTest --key-file E:\migration.key --dry-run
migrate.exe restore --bundle E:\MigrationRepo --destination C:\Users\MigrationTest --key-file E:\migration.key --install-apps
migrate.exe verify --bundle E:\MigrationRepo --key-file E:\migration.key
```

Do not run `--install-apps` against your production machine during testing. The
adapter may install or upgrade every package in the captured application
manifest.

## Test B: Windows Source to macOS

This cannot be completed in Hyper-V. Use the new physical Mac as the
destination. The expected result is:

- Files and portable settings restore.
- Windows-only applications are reported as unavailable.
- Curated equivalent applications are planned through the macOS adapter.
- No Windows executable, registry state, driver, or license activation is
  copied to macOS.

The code includes Homebrew parsing, Linux package planning, curated
Windows-to-macOS mappings, and portable Git identity settings. A complete
Windows-to-Mac test still requires a physical Mac because Hyper-V cannot run or
emulate macOS, and this Windows host cannot validate Mac package-manager or
filesystem behavior.
