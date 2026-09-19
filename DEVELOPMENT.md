# Development

## Local checks

Run the same checks used before committing:

```sh
gofmt -w cmd internal
go test ./...
go vet ./...
git diff --check
```

Inspect the CLI with:

```sh
go run ./cmd/dotpkg --help
```

## Docker development

The Compose development container mounts the source tree at `/workspace` and
keeps Go module and build caches in named volumes:

```sh
docker compose up -d --build dev
docker compose exec dev go test ./...
docker compose exec dev go vet ./...
docker compose exec dev bash
docker compose down
```

The multi-stage Dockerfile also supports isolated test and static runtime
builds:

```sh
docker build --target test .
docker build --target runtime --build-arg VERSION=v0.1.0-experimental.3 -t dotpkg:dev .
```

The runtime image contains only the static executable. Package reconciliation
still requires host tools such as `pacman`, `yay`, and `sudo`.

## CLI usage

```text
dotpkg validate --manifest packages.yaml --profile desktop
dotpkg plan --manifest packages.yaml --profile desktop --state-file state.yaml
dotpkg sync --manifest packages.yaml --profile desktop --state-file state.yaml
dotpkg add lm_sensors --manifest packages.yaml --scope desktop
```

Host overlays are supplied with `--host PATH`. Use `plan` before `sync` to
inspect changes without modifying the system:

```sh
dotpkg plan --manifest packages.yaml --profile desktop
dotpkg sync --manifest packages.yaml --profile desktop --yes
```

## Dotfiles integration

The dotfiles repository keeps package reconciliation in Bash by default. To
enable the pinned standalone package stage explicitly:

```sh
DOTPKG_PACKAGE_STAGE=1 ./linux/install/sync --desktop
```

The adapter downloads and checksum-verifies the pinned release into an ignored
cache, then invokes `dotpkg sync` with the existing manifest and shared state
paths. `--packages-only` stops after that package stage; a normal opt-in sync
continues with the Bash-owned groups, configuration links, and services.

The integration is pinned to a release rather than downloading `latest`. See
[RELEASE.md](RELEASE.md) for asset verification details.

## Release builds

GitHub Actions tests and vets every push. Version tags build static binaries
for `linux/amd64` and `linux/arm64`, publish `SHA256SUMS`, and mark hyphenated
versions as prereleases. See [RELEASE.md](RELEASE.md) for verification.

The project plan and compatibility boundary are tracked in [PLAN.md](PLAN.md).
