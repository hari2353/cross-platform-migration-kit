# Capability Map: Cross-Platform Migration Kit

The product is decomposed before implementation so each capability can be
tested independently and the dependency direction stays one-way.

| Module id | Responsibility | Depends on |
|---|---|---|
| `migration-contract` | Versioned manifest, profile, path, archive, and compatibility contracts | None |
| `source-inventory` | Detect platform, users, files, applications, settings, secrets, and exclusions | `migration-contract` |
| `secure-bundle` | Create and verify an encrypted repository using bundled open-source code | `migration-contract`, `source-inventory` |
| `package-adapters` | Export and restore applications through OS-native package managers | `migration-contract` |
| `config-adapters` | Restore portable configuration using allowlisted, templated recipes | `migration-contract`, `secure-bundle` |
| `destination-restore` | Preflight, plan, execute, resume, and verify a restore on a new machine | `secure-bundle`, `package-adapters`, `config-adapters` |
| `doctor-report` | Report skipped items, conflicts, unsupported settings, and verification results | `source-inventory`, `destination-restore` |

## Build Order

1. `migration-contract`
2. `source-inventory`
3. `secure-bundle`
4. `package-adapters`
5. `config-adapters`
6. `destination-restore`
7. `doctor-report`

## Explicit Boundaries

- Cross-platform personal data and selected portable configuration are in scope.
- The default profile discovers all readable user-owned data roots, subject to
  protected-system, volatile-cache, mounted-external-drive, and credential
  exclusions. It reports every excluded or unreadable root.
- Application intent is portable; binaries are not. Applications must be
  installed using a destination-specific adapter or explicitly skipped.
- OS kernel, drivers, machine identifiers, license activations, hardware
  security keys, and arbitrary system databases are not copied.
- Secrets are excluded by default and require explicit, documented export and
  import adapters. The kit must never scrape passwords from a browser or OS
  credential store.
- The core has no runtime dependency on a vendor, cloud service, installed
  interpreter, external encryption executable, third-party CLI, or network.
  Open-source components may be statically linked or vendored with license
  attribution.
- A restore is dry-run first and never silently overwrites an existing file.
