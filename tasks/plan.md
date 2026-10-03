# Implementation Plan: Cross-Platform Migration Kit

## Overview

Build a local-first, encrypted migration workflow in thin vertical slices. The
first release produces a self-contained executable and proves a safe,
synthetic-file migration end to end before enabling real package-manager
installation or application-specific recipes.

## Architecture Decisions

- Use a versioned manifest as the stable contract between source and destination.
- Use open-source, license-compatible cryptographic/storage components, linked
  or vendored into the executable. Do not require a separately installed
  encryption tool, interpreter, cloud service, or package manager.
- Treat files, application intents, and portable configuration as separate data
  classes with separate adapters and failure reporting.
- Keep package-manager and OS behavior behind adapters; the core remains
  platform-neutral.
- Make encryption and integrity verification mandatory for real capture output.
- Make restore plan-first, non-destructive by default, and resumable.
- Prefer a clear partial-success report over an all-or-nothing migration.
- Never restore raw system state across operating systems.

## Dependency Graph

```text
migration-contract
        |
source-inventory ---- secure-bundle (bundled OSS engine)
        |                    |
        +------------ package-adapters
                             |
                     config-adapters
                             |
                     destination-restore
                             |
                       doctor-report
```

## Task List

### Phase 0: Contract and Safety Foundation

1. Define manifest/profile schemas, versioning, and command exit codes.
2. Define path selection, exclusion, symlink, and conflict invariants.
3. Add synthetic fixtures and threat-model tests before filesystem operations.

### Checkpoint: Contract

- Manifest round-trips deterministically.
- Invalid paths, unsupported schema versions, and unsafe profile values fail.
- Existing destination files are never modified by a plan.

### Phase 1: File Migration Vertical Slice

4. Implement broad user-data inventory with protected system/credential/cache
   exclusions and explicit root decisions.
5. Select and document license-compatible open-source encryption/storage
   components and link or vendor them into the standalone executable.
6. Implement encrypted repository metadata, manifest, and checksums without an
   external runtime executable.
7. Implement inspect and dry-run restore into a temporary destination.
8. Implement copy, conflict handling, resume state, and verification.

### Checkpoint: File Migration

- Synthetic capture, inspect, dry-run, restore, resume, and verify pass on the
  current platform using only the built executable and a local external-drive
  directory.
- Path traversal, symlink escape, interruption, corruption, and conflict tests
  pass.

### Phase 2: Platform Application Adapters

9. Add a read-only Windows `winget export` adapter with fake-command tests.
10. Add a read-only macOS Homebrew Bundle adapter with fake-command tests.
11. Add Linux package inventory and install adapters.
12. Add destination install planning with explicit approval and partial failure.

### Checkpoint: Applications

- No real package manager is invoked by tests.
- Unsupported application intents are reported and do not block file restore.

### Phase 3: Portable Configuration

13. Add reviewed recipes for Git, shell, SSH config metadata without private
    keys, and one editor configuration.
14. Add OS-specific path templating and secret exclusion tests.

### Phase 4: UX and Release Quality

15. Add human-readable and JSON reports with redaction.
16. Add `doctor`, actionable exit codes, and resumable failure guidance.
17. Add Windows/macOS/Linux CI contract checks and release documentation.

## Risks and Mitigations

| Risk | Impact | Mitigation |
|---|---|---|
| Users expect a disk clone | High | State the product boundary in CLI and docs; report unsupported state |
| Sensitive data leaks to repository or logs | Critical | Exclusion by default, bundled authenticated encryption, redaction, security tests |
| Path traversal or symlink escape | Critical | Root validation, no unsafe link following, adversarial fixtures |
| Cross-platform setting mismatch | High | Reviewed recipes, templates, explicit skipped-item report |
| Package manager changes or unavailable apps | Medium | Adapter contracts, fake executables, partial-success semantics |
| Large files exhaust memory or space | Medium | Streaming archive, free-space preflight, size limits |
| Interrupted restore leaves ambiguity | Medium | Per-item state, checksums, safe resume, verification command |

## Verification Commands

The implementation will establish exact commands in the selected build system;
planned checks are:

```text
<project-test-command>
<project-build-command>
migrate --help
migrate doctor
```

## Open Questions

- Select the bundled open-source storage/encryption components, verify licenses,
  and record source/checksums before Task 5.
- Confirm protected-path rules before Task 4.
- Decide whether package installation is opt-in per adapter or one explicit
  `--install-apps` flag before Task 12.
