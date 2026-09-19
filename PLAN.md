# dotpkg Extraction Plan

## Goal

Extract the package-management portion of
[`lukelex/dotfiles`](https://github.com/lukelex/dotfiles) into this standalone
project, distributed as a static Linux executable from GitHub.

The existing Bash implementation must remain untouched until the standalone
project covers the behavior currently used by the dotfiles repository.

## Current repositories

- Standalone project: <https://github.com/lukelex/dotpkg>
- Consumer dotfiles: <https://github.com/lukelex/dotfiles>
- Local standalone checkout: `/home/lukas/dotpkg`
- Local dotfiles checkout: `/home/lukas/dotfiles`

The GitHub CLI is authenticated as `lukelex` and can create and publish
repositories/releases.

## Completed

- [x] Created the public `lukelex/dotpkg` repository.
- [x] Added a Go CLI in `cmd/dotpkg`.
- [x] Added YAML manifest loading with deep host-overlay merging.
- [x] Preserved the current manifest shape used by `linux/packages.yaml`.
- [x] Implemented profile/category selection for desktop and server profiles.
- [x] Implemented optional package selections.
- [x] Implemented package origin resolution (`repo` and `aur`).
- [x] Implemented package planning and managed-package state.
- [x] Implemented legacy state migration from the old `packages` state file.
- [x] Implemented an Arch backend using `pacman`, `yay`, AUR RPC, and `makepkg`-
  based `yay` bootstrapping.
- [x] Added commands:

  ```text
  dotpkg validate
  dotpkg plan
  dotpkg sync
  dotpkg add
  dotpkg version
  ```

- [x] Added compatibility aliases such as `--desktop`, `--server`, and `--check`.
- [x] Added tests for manifest merging, state handling, reconciliation, and the
  current dotfiles manifest.
- [x] Confirmed the standalone planner sees exactly 142 package names for the
  current desktop manifest with all selections enabled.
- [x] Added GitHub Actions CI and static release builds for:

  ```text
  linux/amd64
  linux/arm64
  ```

- [x] CI passes on GitHub.

## Important boundary

The first extraction should replace only package reconciliation. The current
dotfiles `sync` command also handles resources that should remain in the
dotfiles repository initially:

- Unix group membership
- config symlinks
- system and user services
- desktop resource setup
- other system configuration

Do not replace `linux/install/sync`, `linux/install/package`, or
`linux/install/validate-packages` yet.

## Current compatibility contract

The existing package manifest contains package metadata beyond package names:

```yaml
package-name:
  source: repo
  groups: [...]
  configs: [...]
  services:
    system: [...]
    user: [...]
```

`dotpkg` must preserve and ignore unknown metadata until the dotfiles resource
stages are migrated. The current shared state shape is:

```yaml
version: 1
current:
  profile: desktop
  host: ""
  manifest_sha256: ...
  selections: ...
managed:
  packages: [...]
  groups: [...]
  configs: [...]
  services: [...]
```

The package executable should only modify `current.profile`, `current.host`,
`current.manifest_sha256`, `current.selections`, and `managed.packages` when it
is used against the existing dotfiles state file.

## Remaining work before a swap

### 1. Complete behavioral parity

- [x] Match current interactive prompts and defaults exactly.
- [x] Match current dry-run output and confirmation behavior closely enough for
  existing workflows.
- [x] Support all current `--host NAME` semantics, including host overlay paths
  and state recording.
- [x] Confirm package addition behavior for base manifests and host overlays.
- [x] Confirm package removal behavior for desktop and server profiles.
- [x] Confirm first-run adoption behavior for already-installed packages.
- [x] Preserve state fields owned by the dotfiles resource stages.
- [x] Add a lock to prevent concurrent manifest/state updates.

### 2. Test the Arch backend

Add tests with fake command runners and mocked HTTP:

- [x] `pacman -Q` installed/not-installed handling
- [x] repository package listing
- [x] AUR RPC batching and error handling
- [x] repository installation
- [x] AUR installation
- [x] missing-`yay` bootstrap
- [x] desktop/server removal commands
- [x] command argument safety

Do not require a real Arch host or live AUR access in CI.

### 3. Add integration fixtures

Create fixtures representing:

- [x] the current base manifest
- [x] a host overlay
- [x] a legacy state directory
- [x] a current shared state file
- [x] selected and unselected desktop options

- [x] Compare the standalone package lists against the Bash/yq implementation until
  the lists, origins, state transitions, and planned changes match.

### 4. Define the integration adapter

Once parity is established, add a dotfiles-side compatibility wrapper that:

- [ ] locates the pinned `dotpkg` binary
- [ ] passes the existing manifest and state paths explicitly
- [ ] translates existing profile/host flags
- [ ] invokes `dotpkg sync` for the package stage only
- [ ] leaves groups, configs, and services in Bash

- [ ] Keep the wrapper opt-in initially, for example through an environment
variable or an explicit installer flag. Do not silently switch the default.

### 5. Migrate resource stages or formalize the boundary

- [ ] Decide whether groups, config links, and services belong in `dotpkg` or remain
dotfiles-specific. If they move into `dotpkg`, add separate resource modules
and tests rather than coupling them directly to package installation.

- [ ] Do not switch the eventual full sync until these stages have parity.

### 6. Release process

The workflow currently builds release assets on `v*` tags. Before the first
stable release:

- [ ] add checksums and release verification documentation
- [ ] consider GitHub artifact attestations or signed releases
- [ ] publish an explicitly experimental prerelease first
- [ ] document that “universal Linux” means libc-independent per-architecture
  binaries; one ELF cannot run on both x86-64 and ARM64
- [ ] pin the release version from dotfiles rather than downloading `latest`

The binary still requires host tools such as `pacman`, `yay`, `sudo`, and
possibly `systemd`; only the `dotpkg` executable is self-contained.

## Useful commands

From `/home/lukas/dotpkg`:

```sh
go test ./...
go vet ./...
go run ./cmd/dotpkg --help
git status
gh run list --repo lukelex/dotpkg
```

Build locally:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags='-s -w' -o /tmp/dotpkg-linux-amd64 ./cmd/dotpkg

CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -trimpath -ldflags='-s -w' -o /tmp/dotpkg-linux-arm64 ./cmd/dotpkg
```

Exercise the current dotfiles manifest without changing the system by using a
state fixture with all desktop selections recorded and a fake `pacman` whose
`-Q` command returns not-installed. The existing dotfiles repository must not
be modified during these checks.

## Safety rule

Before changing the dotfiles repository, verify all of the following:

1. [ ] `dotpkg` tests and CI pass.
2. [ ] Package names and origins match the Bash implementation.
3. [ ] State migration and ownership/removal behavior match.
4. [ ] A dry-run comparison shows equivalent changes.
5. [ ] The integration is opt-in and reversible.
6. [ ] The Bash implementation remains available as the fallback.
