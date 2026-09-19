# dotpkg

`dotpkg` is a manifest-driven Linux package reconciler. It is being extracted
from the package portion of [lukelex/dotfiles](https://github.com/lukelex/dotfiles).

The first backend targets Arch Linux and uses `pacman` for repository packages
and `yay` for AUR packages. The executable itself is intended to be a static,
cross-distribution Linux binary; package backends remain distribution-specific.

## Current status

This is the standalone extraction phase. It does **not** replace the Bash
installer yet. The existing dotfiles installer remains the source of truth
until this project covers its behavior and migration tests.

## Commands

```text
dotpkg validate --manifest packages.yaml --profile desktop
dotpkg plan --manifest packages.yaml --profile desktop --state-file state.yaml
dotpkg sync --manifest packages.yaml --profile desktop --state-file state.yaml
dotpkg add lm_sensors --manifest packages.yaml --scope desktop
```

Host overlays are supplied with `--host PATH`. The state file can be supplied
explicitly so an integration can continue using an existing state format.

## Development

```sh
go test ./...
go run ./cmd/dotpkg --help
```

The project will publish static `linux/amd64` and `linux/arm64` release assets
with checksums. A single ELF cannot run on multiple CPU architectures, so
“universal Linux binary” means one libc-independent binary per architecture.
