# Research and Prior Art

The product direction is based on existing, maintained platform capabilities.
The project should orchestrate and add safety/reporting rather than replace
these tools without a concrete reason.

## Kopia

Kopia is the closest existing repository to the data-transfer foundation. Its
official documentation describes a cross-platform backup tool for Windows,
macOS, and Linux with client-side end-to-end encryption. Its feature
documentation says snapshots are encrypted before storage, both content and
file names are encrypted, unencrypted repositories are not allowed, local
storage is supported, and restore plus consistency verification are available.

Sources:

- [Kopia repository](https://github.com/kopia/kopia)
- [Kopia features](https://kopia.io/docs/features/)
- [Kopia installation](https://kopia.io/docs/installation/)

Implication for this project: use Kopia as research and implementation prior
art, but do not make its executable a runtime requirement. The migration
project must ship its own self-contained open-source data engine or bundle a
license-compatible implementation.

## Apple Migration Assistant

Apple documents Migration Assistant as transferring documents, apps, user
accounts, and settings between Macs. Apple also documents migration from a
Windows PC and from a Time Machine backup. It does not replace the operating
system on the destination and does not delete the old Mac.

Source: [Transfer to a new Mac with Migration Assistant](https://support.apple.com/en-us/102613)

Implication for this project: same-platform Mac migration is already well
served. Cross-platform work should focus on a portable manifest, explicit
translation, and a report of unsupported settings rather than pretending to be
a disk clone.

## Windows Package Manager

Microsoft documents `winget export` as producing a JSON list of applications
that can be used with `winget import` for batch installation. Export matching
depends on package metadata and may warn when an installed application cannot be
matched.

Source: [Microsoft Learn: `export` command](https://learn.microsoft.com/en-us/windows/package-manager/winget/export)

Implication for this project: application migration must be represented as
install intent, not copied application binaries, and unmatched packages must be
first-class report items.

## Homebrew Bundle

Homebrew documents `brew bundle dump` as an installed-state snapshot into a
`Brewfile` and `brew bundle` as the declarative install operation. Homebrew
Bundle supports formulae, casks, Mac App Store apps, and several other package
types, but it is not a universal Windows/macOS application mapping system.

Source: [Homebrew Bundle documentation](https://docs.brew.sh/Homebrew-Bundle-and-Brewfile)

Implication for this project: macOS package state belongs in a native adapter;
the adapter must not claim that every Windows application has a Mac equivalent.

## chezmoi

chezmoi provides cross-platform dotfile management with templates, encrypted
files, machine-specific differences, and scripts. It is strong prior art for
portable configuration but is intentionally focused on dotfiles rather than
full personal-data and application migration.

Source: [chezmoi](https://www.chezmoi.io/)

Implication for this project: portable settings should use reviewed recipes and
templates, with secrets excluded by default. The migration kit must not
silently import arbitrary scripts as part of a restore.

## Other Relevant Repositories

- [restic](https://github.com/restic/restic) is a mature encrypted backup
  program supporting Windows, macOS, Linux, local storage, restore, and
  verification. It is a viable implementation reference.
- [Syncthing](https://github.com/syncthing/syncthing) is continuous peer-to-peer
  file synchronization with strong safety goals. It is not a one-time encrypted
  migration repository and does not translate applications or settings.
- [rclone](https://github.com/rclone/rclone) moves data across local, network,
  and cloud providers and offers optional encryption. It is a transport layer,
  not a whole-computer migration planner.
- [Homebrew](https://github.com/Homebrew/brew) and Windows Package Manager
  provide native package ecosystems, but neither defines cross-OS application
  equivalence or a complete migration workflow.

## Conclusion

Existing repositories solve important parts, especially encrypted backup. No
repository reviewed here combines encrypted broad user-data migration,
application equivalence, settings translation, dry-run restore, and a
cross-platform compatibility report. We should integrate ideas without
requiring those projects at runtime.
