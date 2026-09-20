<p align="center">
  <img src="assets/dotpkg.svg" alt="dotpkg logo" width="112">
</p>

<h1 align="center">dotpkg</h1>

`dotpkg` is a standalone, manifest-driven Linux package reconciler extracted
from [lukelex/dotfiles](https://github.com/lukelex/dotfiles). Its goal is to
make package installation reproducible, reviewable, and safe to integrate into
larger system provisioning workflows.

## High-level mechanisms

- **Manifest selection:** YAML manifests describe common packages, desktop or
  server profiles, optional selections, package origins, and host overlays.
- **Reconciliation:** The planner compares declared packages with the installed
  system and managed ownership state, producing adopt, install, and remove
  changes. Removal is restricted to packages previously recorded as managed.
- **State preservation:** Package ownership, selections, profile, host, and
  manifest identity are recorded atomically. Existing shared state retains
  resource-stage fields that dotpkg does not own.
- **Backend isolation:** Distribution-specific behavior lives behind a backend
  factory and interface. The current Arch backend uses `pacman`, `yay`, and the
  AUR RPC; Debian/Ubuntu (`apt`) and Fedora/RHEL (`dnf`) backends are also
  available without changing manifest or planning logic.
- **Safe execution:** Plans can be inspected before application, updates are
  serialized with owner diagnostics, bounded by command and HTTP timeouts, and
  package changes have a recovery journal, and the executable is built as a
  static per-architecture Linux binary.

The default integration owns package reconciliation plus separately tested
groups, configuration links, and service resource stages. Package-only operation
is available through `sync --packages-only` and `package sync`.

See [DEVELOPMENT.md](DEVELOPMENT.md) for CLI usage, development commands,
Docker workflows, integration instructions, and release details.
