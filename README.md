<p align="center">
  <img src="assets/dotpkg.svg" alt="dotpkg logo" width="112">
</p>

<h1 align="center">dotpkg</h1>

`dotpkg` is a standalone, manifest-driven Linux package reconciler extracted
from [lukelex/dotfiles](https://github.com/lukelex/dotfiles). It makes package
installation reproducible, reviewable, and safe to integrate into larger Linux
provisioning workflows without requiring the full dotfiles repository.

## High-level mechanisms

- **Manifest selection:** YAML manifests describe common packages, desktop or
  server profiles, optional selections, package origins, host overlays, and
  compatible metadata for other dotfiles stages.
- **Reconciliation:** The planner compares declared packages with the installed
  system and managed ownership state, producing adopt, install, and remove
  changes. Removal and `clean` are restricted to packages previously recorded
  as managed; unrelated installed packages are never removed.
- **State preservation:** Package ownership, selections, profile, host, and
  manifest identity are recorded atomically. State migrations are versioned and
  preserve unrelated fields, while the previous state is retained as a `.bak`
  backup.
- **Backend isolation:** Distribution-specific behavior lives behind a backend
  factory and interface. The current Arch backend uses `pacman`, `yay`, and the
  AUR RPC; Debian/Ubuntu (`apt`) and Fedora/RHEL (`dnf`) backends are also
  available without changing manifest or planning logic. AUR packages are
  intentionally unsupported on the Debian and Fedora backends.
- **Safe execution:** Plans can be inspected before application, updates are
  serialized with lock-owner diagnostics, and command and network operations
  are bounded by timeouts. Package changes use a crash-recovery journal with
  compensating operations and can be resumed with `dotpkg recover`.
- **Diagnostics and automation:** `doctor` reports package, availability,
  state, group, config, service, and backend problems. Plans and diagnostics
  support machine-readable JSON, with the plan format versioned in
  [`schema/plan-v1.json`](schema/plan-v1.json).
- **Reproducible releases:** Release artifacts are static Linux binaries for
  amd64 and arm64 and are published with SHA256 checksums.

## Scope

The standalone command owns package reconciliation by default. With
`--resources`, it can also reconcile the explicitly declared groups,
configuration links, and systemd services used by the dotfiles integration.
Those resource operations are safety-checked and tracked in shared state, but
dotpkg does not attempt to manage arbitrary files, shell configuration, or
unrelated dotfiles resources. Existing manifest metadata that belongs to other
stages is preserved rather than redesigned.

The dotfiles adapter also exposes `--packages-only` when it needs to stop after
the package stage. Package changes are previewed with `plan` or `diff`; resource
changes can be included in the same plan with `--resources`.

## Quick start

```sh
dotpkg validate --manifest packages.yaml --profile desktop
dotpkg plan --manifest packages.yaml --profile desktop
dotpkg sync --manifest packages.yaml --profile desktop --yes
dotpkg doctor --manifest packages.yaml --profile desktop
```

Use `clean --yes` only when removing stale managed packages is intended. If a
transaction is interrupted and a journal remains beside the state file, review
it and run `dotpkg recover --yes`.

See [DEVELOPMENT.md](DEVELOPMENT.md) for CLI usage, development commands,
Docker workflows, integration instructions, and release details.
