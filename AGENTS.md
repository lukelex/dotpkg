# AGENTS.md

## Project

`dotpkg` is a standalone Go package reconciler extracted from
`lukelex/dotfiles`. It currently targets Arch Linux through `pacman` and
`yay`, while the executable is built as a static Linux binary.

## Development

Run these before committing:

```sh
gofmt -w cmd internal
go test ./...
go vet ./...
git diff --check
```

The GitHub workflow runs `go test ./...` and `go vet ./...`. Release builds
are produced only for `v*` tags and currently target `linux/amd64` and
`linux/arm64` with `CGO_ENABLED=0`.

## Architecture

- `cmd/dotpkg` — CLI entrypoint and flag compatibility.
- `internal/manifest` — YAML parsing, host overlays, package metadata, and
  manifest updates.
- `internal/state` — managed package state and migration from the legacy
  dotfiles state format.
- `internal/reconcile` — profile selection, planning, validation, sync, and
  ownership tracking.
- `internal/backend` — package-manager integration. Keep distribution-specific
  behavior here rather than in the manifest/planner layers.
- `testdata` — compatibility fixtures derived from the current dotfiles
  manifest.

## Compatibility boundary

Do not modify or replace the Bash implementation in
`/home/lukas/dotfiles` until the checklist in `PLAN.md` is complete.

The first integration may replace only package reconciliation. Groups, config
links, systemd services, and other dotfiles resource stages remain owned by the
dotfiles repository until explicitly migrated and tested.

The existing manifest may contain package metadata such as `groups`,
`configs`, and `services`. Preserve that data and ignore fields not needed by
the package backend; do not redesign the manifest casually.

When using the existing dotfiles state file, `dotpkg` may update only package
state and package-selection fields. Preserve group, config, and service state.

## Safety rules

- Never run package installation or removal during tests.
- Use fake command runners and mocked HTTP for backend tests.
- Do not use live AUR access in CI.
- Package removal must remain limited to packages recorded as managed.
- Keep manifest and state writes atomic.
- Do not download or execute `latest` releases from the dotfiles integration;
  pin a release version and verify checksums.
- Do not commit generated binaries, local state, or secrets.

## GitHub

Repository: <https://github.com/lukelex/dotpkg>

The authenticated GitHub account is `lukelex`. Use normal commits and pushes
to `master`; tag-based releases are reserved for reviewed, compatibility-safe
versions.
