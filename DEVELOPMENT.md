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
docker build --target runtime --build-arg VERSION=v0.3.0 -t dotpkg:dev .
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

Use `--manifest PATH` to select a different manifest, or set
`DOTPKG_MANIFEST=PATH` for a wrapper-wide default. Resource paths still resolve
from the inferred manifest root unless `--root PATH` is supplied explicitly.

Host overlays are supplied with `--host PATH`. Use `plan` before `sync` to
inspect changes without modifying the system:

```sh
dotpkg plan --manifest packages.yaml --profile desktop
dotpkg sync --manifest packages.yaml --profile desktop --yes
```

## Custom resources

Resource metadata may be attached to packages or declared independently. Use
explicit resources for configuration links and services that are not owned by
a package:

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

`profiles` and `selections` are optional. A resource is declared only when its
profile matches and every listed selection is enabled. Service scopes are
`system` or `user`; user services are tracked as `user:<name>` in state. Unit
files linked under systemd directories trigger the appropriate daemon reload,
and system service changes run through `sudo`.

Manifest fields consumed by dotpkg are structurally validated before planning.
Unknown metadata is preserved for compatibility with the surrounding dotfiles
manifest.

## Dotfiles integration

The dotfiles repository uses the pinned standalone package and resource stages
by default. To invoke them explicitly:

```sh
./linux/install/sync --desktop
```

The adapter downloads and checksum-verifies the pinned release into an ignored
cache, then invokes `dotpkg sync` with the existing manifest and shared state
paths. A normal sync delegates groups, configuration links, and services too.
`--packages-only` stops after the package stage.

The integration is pinned to a release rather than downloading `latest`. See
[RELEASE.md](RELEASE.md) for asset verification details.

## Release builds

GitHub Actions tests and vets every push. Version tags build static binaries
for `linux/amd64` and `linux/arm64`, publish `SHA256SUMS`, and mark hyphenated
versions as prereleases. See [RELEASE.md](RELEASE.md) for verification.

The project plan and compatibility boundary are tracked in [PLAN.md](PLAN.md).
