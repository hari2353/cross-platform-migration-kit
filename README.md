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

## Planned Commands

The command names are part of the initial contract and may be refined during
implementation:

```powershell
# On the source computer, with the external drive mounted:
migrate capture --source C:\Users\you --output E:\migration-repository --key-file E:\migration.key --profile personal --apps

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

## Security Warning

The repository may contain personal documents and application configuration.
Use the tool's authenticated encryption, keep the repository password/key
separate from the drive, and verify the repository before deleting the source.
Never commit a real repository, key, credential export, or personal
configuration to GitHub.

## Development

The current implementation is Go 1.26 with only the standard library. The
standalone binary is built with `CGO_ENABLED=0` and does not require Go after
compilation.

## License

Apache-2.0. See [LICENSE](LICENSE).
