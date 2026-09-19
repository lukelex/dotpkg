# dotpkg

`dotpkg` is a manifest-driven Linux package reconciler. Declare the packages a
machine should have, preview the difference, and synchronize the system.

It is being extracted from the package portion of
[lukelex/dotfiles](https://github.com/lukelex/dotfiles).

The first backend targets Arch Linux and uses `pacman` for repository packages
and `yay` for AUR packages. The executable itself is intended to be a static,
cross-distribution Linux binary; package backends remain distribution-specific.

## What it does

- Reads YAML package manifests.
- Supports desktop/server profiles and optional package groups.
- Supports host-specific manifest overlays.
- Validates repository and AUR package names.
- Installs missing declared packages.
- Removes only packages previously tracked as managed.
- Records ownership and selections in a state file.
- Provides dry-run planning before changes are applied.

For example, a manifest can group packages by profile:

```yaml
source: aur
common:
  packages:
    headless:
      git: {}
profiles:
  desktop:
    packages:
      desktop:
        neovim: {}
```

`dotpkg` compares that declaration with the installed system and produces a
plan containing packages to install, remove, or adopt.

## Current status

This is the standalone extraction phase. It does **not** replace the Bash
installer yet. The existing dotfiles installer remains the source of truth
until this project covers its behavior and migration tests.

The project currently provides an Arch Linux backend. It uses `pacman` for
official repository packages and `yay` for AUR packages. The executable is
designed to be a static Linux binary, while package backends remain
distribution-specific.

The package extraction is not yet wired into the dotfiles installer. Groups,
configuration links, services, and other system setup remain managed by the
original Bash implementation until compatibility work is complete.

## Usage

```text
dotpkg validate --manifest packages.yaml --profile desktop
dotpkg plan --manifest packages.yaml --profile desktop --state-file state.yaml
dotpkg sync --manifest packages.yaml --profile desktop --state-file state.yaml
dotpkg add lm_sensors --manifest packages.yaml --scope desktop
```

Host overlays are supplied with `--host PATH`. The state file can be supplied
explicitly so an integration can continue using an existing state format.

Use `plan` before `sync` when inspecting changes:

```sh
dotpkg plan --manifest packages.yaml --profile desktop
dotpkg sync --manifest packages.yaml --profile desktop --yes
```

Package installation and removal still require the host's package-manager
tools and appropriate privileges. The binary does not replace `pacman`, `yay`,
or `sudo`.

## Downloads

Tagged GitHub releases will provide static binaries for:

```text
linux/amd64
linux/arm64
```

One ELF binary cannot run on multiple CPU architectures, so “universal Linux
binary” means one libc-independent binary per architecture.

## Development

```sh
go test ./...
go run ./cmd/dotpkg --help
```

Release builds use `CGO_ENABLED=0` and include checksums. Run the test suite
and static analysis before submitting changes:

```sh
go test ./...
go vet ./...
```
