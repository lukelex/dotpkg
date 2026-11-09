# Refactoring backlog

## In progress

- [x] Replace repeated resource-stage lists with a registry that owns stage
  names, managed-state keys, planning, application, cleanup, and rendering.

## Next

- [x] Split group, config-link, and service implementations from
  `internal/resource/resource.go`; remove positional service-stage handling.
- [x] Share user-path expansion and containment validation between config and
  directory resources.
- [x] Make resource clean-plan construction report declaration/discovery
  failures rather than suppressing them.
- [x] Extend `doctor` with executable-link and directory diagnostics.
- [x] Centralize managed-state list keys in `internal/state`.
- [x] Split package sync and clean from `internal/reconcile/reconcile.go`.
  - [x] Move plan-document and text rendering to `plan.go`.
  - [x] Move cleanup orchestration to `clean.go`.
  - [x] Move sync and recovery orchestration to `sync.go`.

Do not introduce a generic resource abstraction that erases resource-specific
safety rules. Prefer a shared orchestration registry and focused resource
modules.

## Go quality practices

- [x] Run pinned `staticcheck` in CI.
- [x] Run the Go race detector in a dedicated CI job.
- [x] Add fuzz coverage for manifest parsing and resource path handling.
- [x] Add a golden fixture for the versioned plan document.
- [x] Run pinned `govulncheck` in CI.
- [ ] Publish coverage reports without a rigid coverage threshold.
- [ ] Consolidate test fakes if further resource types make duplication costly.
