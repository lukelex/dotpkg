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

## Installation

Releases provide libc-independent static binaries for `linux/amd64` and
`linux/arm64`. Download a pinned release and verify it before installing:

```sh
version=v0.7.0
curl --fail --location --remote-name \
  "https://github.com/lukelex/dotpkg/releases/download/${version}/dotpkg-linux-amd64"
curl --fail --location --remote-name \
  "https://github.com/lukelex/dotpkg/releases/download/${version}/SHA256SUMS"
sha256sum --ignore-missing --check SHA256SUMS
chmod 0755 dotpkg-linux-amd64
sudo install -m 0755 dotpkg-linux-amd64 /usr/local/bin/dotpkg
```

Use `dotpkg-linux-arm64` on ARM64 hosts. The binary does not include a package
manager or system tools: Arch operation requires `pacman` and normally `sudo`,
Debian/Ubuntu requires `apt`/`dpkg`, and Fedora/RHEL requires `dnf`/`rpm`.
Resource reconciliation additionally uses `id`, `getent`, and `systemctl`.
See [RELEASE.md](RELEASE.md) for release verification details.

## Manifest and selections

The manifest keeps the dotfiles-compatible YAML shape. Packages may be grouped
under `common` and `profiles`, with optional selections and per-package
metadata:

```yaml
source: repo
common:
  packages:
    base:
      git:
      ripgrep:
profiles:
  desktop:
    packages:
      graphical:
        firefox:
          source: repo
        yay-tool:
          source: aur
    options:
      i3:
        default: false
        packages:
          i3:
```

Package metadata can declare `source: repo|aur|appimage`, group membership, config
links, and services. AppImage nodes use `source: appimage` with an HTTPS
`address`; their integrity metadata is resolved automatically. A host file supplied with `--host PATH` is deep-merged
over the manifest. Use `--manifest PATH` or `DOTPKG_MANIFEST=PATH` to select
the manifest; resource paths resolve relative to its directory unless
`--root PATH` is supplied.

## CLI and automation

The command set is:

```text
validate   Validate manifest packages and backend availability
plan       Show package changes without applying them
diff       Read-only alias for plan
sync       Apply package changes and, optionally, resources
clean      Remove stale managed packages and resources
recover    Roll back a pending package transaction
add        Declare, validate, install, and track one package
doctor     Produce read-only diagnostics
completion Generate Bash, Zsh, or Fish completion
version    Print the executable version
```

Use `--output json` with `plan`, `diff`, `sync`, or `clean` for a unified,
versioned plan document. The schema is [plan-v1.json](schema/plan-v1.json) and
covers package, group, config, and service actions. `doctor --output json`
provides a machine-readable diagnostic report.

Common operational flags include `--dry-run`/`--check`, `--yes`,
`--profile desktop|server`, `--resources`, `--replace`,
`--restart-services`, and `--timeout`. Commands prompt before changes by
default. `clean` never adopts or installs packages and removes only stale items
recorded as managed.

## Resources and safety

Resource reconciliation is opt-in with `--resources` and covers only declared
Unix groups, configuration links, and systemd services. Resources can be
declared independently or attached to packages:

```yaml
resources:
  configs:
    - source: linux/config/my-tool
      target: $XDG_CONFIG_HOME/my-tool
      profiles: [desktop]
      selections: [i3]
  services:
    - name: my-tool.service
      scope: user
      profiles: [desktop]
      selections: [i3]
```

Config sources must remain inside the configured root, targets are restricted
to approved home/XDG locations, and broken or escaping symlinks are rejected.
Existing conflicting targets are preserved unless `--replace` is supplied.
System services use `sudo`; user services are tracked separately. Config-linked
services are restarted only with the explicit `--restart-services` flag, and
systemd daemon reloads are performed when unit files change.

AppImages are handled as a separate managed artifact stage rather than passed
to `pacman`, `apt`, or `dnf`. The node supplies an HTTPS `address`; a checksum
does not need to be written manually. GitHub release assets use their published
SHA256 digest, while other hosts must expose a `.sha256`/`.sha256sum` sidecar
or `.zsync` metadata. Version-pinned addresses are required and `latest` is
rejected. The default target is `$HOME/.local/bin/<node-name>`; explicit
targets must stay inside the user home directory.

## State, recovery, and compatibility

The default state file is `$XDG_STATE_HOME/dotpkg/state.yaml`. It records
managed packages and origins, selections, profile, host, manifest identity, and
resource ownership. Writes are atomic. Older state files and the legacy sibling
`packages` file are migrated automatically; the previous YAML state is retained
as `state.yaml.bak`. Fields belonging to other dotfiles stages are preserved.

Package-changing operations create a journal beside the state file before
installing or removing packages. If an operation fails, dotpkg attempts
compensating installs/removals and restores state. If the journal remains after
an interruption, inspect it and run `dotpkg recover --yes`; recovery is best
effort and retains the journal when restoration itself fails.

## Backends and AUR behavior

The backend defaults to Arch and can be selected explicitly with
`DOTPKG_BACKEND=arch`, `debian`, or `fedora`. Debian/Ubuntu and Fedora/RHEL
support repository packages only; AUR packages are rejected on those backends.

Arch validation and installation use `pacman`, `yay`, and the AUR RPC. AUR
requests have configurable timeout and retry controls:
`--backend-timeout`, `--aur-retries`, `--aur-retry-delay`, and `--verbose`.
When `yay` is missing, dotpkg bootstraps `yay-git` from the AUR and checks out a
pinned source revision before building it; it never builds an unpinned moving
checkout.

## Dotfiles boundary

The [dotfiles repository](https://github.com/lukelex/dotfiles) consumes a
pinned, checksum-verified dotpkg release through its `linux/install/sync`
adapter. Its normal integration invokes package reconciliation plus the
optional group, config, and service stages; `--packages-only` is an adapter
option for stopping after packages. Other dotfiles responsibilities, such as
unrelated shell/configuration setup, remain outside dotpkg. The adapter pins a
release rather than downloading a moving `latest` binary.

## Development and limitations

Run the local checks with:

```sh
gofmt -w cmd internal
go test ./...
go vet ./...
git diff --check
```

Tests use fake command runners and mocked AUR HTTP; they never install or
remove packages and CI never uses live AUR access. dotpkg currently targets
Linux and the published binaries target only amd64 and arm64. See
[`DEVELOPMENT.md`](DEVELOPMENT.md) for Docker workflows, integration tests,
completion generation, and release procedures.
