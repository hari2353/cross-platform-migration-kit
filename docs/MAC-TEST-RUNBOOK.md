# macOS Test Runbook

This document is the authoritative procedure for testing the migration kit on a
real Mac. A human or coding agent should read this file before running commands.

## Test Objective

Validate a Windows-source to macOS-destination migration using an encrypted
repository on an external drive:

- The Mac builds and runs the standalone binary.
- The Mac can decrypt and inspect the repository.
- Dry-run creates no destination files and installs no applications.
- Personal files restore into a sandbox destination.
- Portable Git identity settings restore without credential helpers or tokens.
- Windows application intents become curated macOS plans where a mapping exists.
- Homebrew and App Store install commands are only executed after explicit
  approval.
- Verification detects missing or modified files.
- `resume` is safe and idempotent.
- `doctor` reports bundle, key, manifest, and destination health.

## Important Limitations

This is not an operating-system clone. The Mac test must not expect:

- Windows executables to run on macOS.
- Windows drivers, registry state, services, or activation state to transfer.
- Every Windows application to have a macOS equivalent.
- Proprietary applications to become open source.
- Browser cookies, passwords, private keys, or credential stores to transfer.

The expected result is partial but explicit compatibility: files and reviewed
settings restore; mapped applications are planned or installed; unsupported
applications appear in the report as manual actions.

## Preconditions

1. Use macOS 13 or later.
2. Confirm whether the Mac is Apple Silicon or Intel:

   ```bash
   uname -m
   ```

   Expected values are `arm64` or `x86_64`.

3. Install Go 1.26 or later only for building from source. The released binary
   itself does not require Go at runtime.
4. Mount the external drive and identify its path:

   ```bash
   ls -la /Volumes
   ```

5. Obtain the encryption key separately from the repository. Do not store the
   key in the Git repository or beside a public backup.
6. For application installation tests, install Homebrew from its official
   source and confirm it is available:

   ```bash
   command -v brew
   brew --version
   ```

   If `brew` is unavailable, continue with file/settings verification and mark
   application installation as `blocked: brew unavailable`.

## Get the Code

```bash
git clone https://github.com/hari2353/cross-platform-migration-kit.git
cd cross-platform-migration-kit
git status --short --branch
```

The worktree should be clean and on `main`.

## Build and Unit-Test

Apple Silicon:

```bash
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build \
  -trimpath -ldflags="-s -w" -o ./migrate ./cmd/migrate
```

Intel:

```bash
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build \
  -trimpath -ldflags="-s -w" -o ./migrate ./cmd/migrate
```

Run the local test suite:

```bash
go test ./... -count=1
./migrate doctor
```

The final `doctor` command is expected to report missing arguments. That proves
the binary starts; it is not the migration test yet.

## Identify the Repository and Key

Set these variables to the real external-drive paths. Do not paste the key
contents into a terminal transcript or issue:

```bash
export MIGRATION_REPO="/Volumes/MigrationDrive/migration-repository"
export MIGRATION_KEY="$HOME/.migration-kit/migration.key"
export TEST_DESTINATION="$HOME/migration-kit-test-destination"
```

Check that the paths exist without printing sensitive data:

```bash
test -d "$MIGRATION_REPO"
test -f "$MIGRATION_KEY"
chmod 600 "$MIGRATION_KEY"
```

If either check fails, stop and report the missing path. Do not generate a new
key unless the source repository was also created with that new key.

## Inspect the Source Bundle

```bash
./migrate inspect \
  --bundle "$MIGRATION_REPO" \
  --key-file "$MIGRATION_KEY" \
  --profile personal
```

Record:

- Source platform.
- Number of files.
- Number of application intents.
- Application managers and identifiers.
- Any exclusions or unsupported entries.

Do not paste personal filenames, document names, full paths, or application
metadata containing private information into a public issue.

## Plan Compatibility Without Installing

Create a fresh sandbox path:

```bash
rm -rf "$TEST_DESTINATION"
mkdir -p "$TEST_DESTINATION"
```

Run the mandatory dry-run:

```bash
./migrate restore \
  --bundle "$MIGRATION_REPO" \
  --destination "$TEST_DESTINATION" \
  --key-file "$MIGRATION_KEY" \
  --profile personal \
  --dry-run
```

Acceptance checks:

```bash
find "$TEST_DESTINATION" -type f -print
```

The destination must still contain no restored files. The command may print
planned file/application results, but it must not invoke Homebrew or install
anything.

## Restore Files and Portable Settings

Only after reviewing the dry-run:

```bash
./migrate restore \
  --bundle "$MIGRATION_REPO" \
  --destination "$TEST_DESTINATION" \
  --key-file "$MIGRATION_KEY" \
  --profile personal
```

Verify the destination:

```bash
./migrate verify \
  --bundle "$MIGRATION_REPO" \
  --destination "$TEST_DESTINATION" \
  --key-file "$MIGRATION_KEY"
```

The output should report restored files as `verified`. If Git identity was
captured, inspect only the safe fields:

```bash
sed -n '/^\[user\]/,/^$/p' "$TEST_DESTINATION/.gitconfig"
```

The restored settings must not contain credential helpers, access tokens,
private keys, or passwords.

## Resume Test

Run resume twice:

```bash
./migrate resume \
  --bundle "$MIGRATION_REPO" \
  --key-file "$MIGRATION_KEY" \
  --profile personal

./migrate resume \
  --bundle "$MIGRATION_REPO" \
  --key-file "$MIGRATION_KEY" \
  --profile personal
```

The second run should classify already-correct files as `already-complete` or
otherwise leave them unchanged. It must not duplicate, truncate, or corrupt
files.

## Application Installation Test

Run this only in a disposable Mac user account or a dedicated test Mac. Do not
install the entire source application inventory on a production Mac without
reviewing every application first.

First inspect the planned application mappings. The runbook expects known
Windows identifiers such as Git, VS Code, Chrome, Firefox, Brave, Node.js, or
VLC to produce reviewed macOS targets when mappings exist. Windows-only or
unknown applications must be reported as unsupported.

The current CLI uses `--install-apps` to execute supported destination commands:

```bash
./migrate restore \
  --bundle "$MIGRATION_REPO" \
  --destination "$TEST_DESTINATION" \
  --key-file "$MIGRATION_KEY" \
  --profile personal \
  --install-apps
```

Application installation may require:

- Network access to Homebrew or App Store sources.
- Package agreements.
- Administrator approval.
- Existing package-manager configuration.
- Manual handling for unsupported applications.

After installation, verify selected applications manually:

```bash
command -v git || true
command -v node || true
command -v code || true
brew list --formula 2>/dev/null || true
brew list --cask 2>/dev/null || true
```

Do not treat every preinstalled dependency or runtime package as a user-facing
application success. Record only the identifiers you explicitly checked.

## Doctor and Final Evidence

```bash
./migrate doctor \
  --bundle "$MIGRATION_REPO" \
  --destination "$TEST_DESTINATION" \
  --key-file "$MIGRATION_KEY"
```

The final report should include:

- Mac model and `uname -m`.
- macOS version from `sw_vers -productVersion`.
- Migration kit commit from `git rev-parse --short HEAD`.
- Build command and result.
- `go test ./...` result.
- Source platform and bundle counts.
- Number of files verified.
- Number of settings restored.
- Application intents planned, installed, skipped, and failed.
- `doctor` result.
- Any manual actions.

Safe evidence commands:

```bash
sw_vers -productVersion
uname -m
./migrate --help 2>&1 || true
```

Never include the encryption key, personal documents, full private paths,
credentials, tokens, private keys, or the contents of the encrypted repository
in the report.

## Failure Handling

- `cipher: message authentication failed`: wrong key or corrupted repository;
  stop and do not overwrite anything.
- `restore requires a completed dry-run`: run the exact dry-run again with the
  same repository, destination, profile, and overwrite policy.
- `brew is unavailable`: continue file verification, mark app installation
  blocked, and do not install Homebrew automatically through this tool.
- Application unavailable: record it as a manual action; do not substitute an
  unreviewed package with a similar name.
- Destination conflict: use a fresh sandbox or explicitly review the conflict;
  never use overwrite casually on a real home directory.

## Agent Instructions

An agent executing this runbook must:

1. Read this entire file before running migration commands.
2. Use a sandbox destination first.
3. Never print or upload key contents or personal file contents.
4. Never run `--install-apps` on a production profile without explicit human
   approval.
5. Preserve the source machine and source repository.
6. Capture command output and summarize counts/statuses, not private paths.
7. Mark physical Mac results separately from simulated or CI results.
8. Stop and ask for confirmation before destructive operations or overwrite.
