# Runtime Dependencies

The core executable currently has no third-party runtime dependencies.

| Component | Version | Runtime mode | License | Source |
|---|---|---|---|---|
| Go standard library | Go 1.26 toolchain for this build | Statically linked into the executable | Go license | https://go.dev/LICENSE |

Optional package-manager adapters may inspect or invoke tools already provided
by the destination operating system. They are not required by capture, inspect,
restore, resume, or verify, and they are never downloaded or installed by the
core executable.

When a future release vendors an external open-source component, add its exact
version, source revision, checksum, license, attribution, and build method to
this file before using it in a release binary.
