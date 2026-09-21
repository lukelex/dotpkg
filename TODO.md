# Refactoring backlog

## In progress

- [x] Replace repeated resource-stage lists with a registry that owns stage
  names, managed-state keys, planning, application, cleanup, and rendering.

## Next

- [ ] Split group, config-link, and service implementations from
  `internal/resource/resource.go`; remove positional service-stage handling.
- [ ] Share user-path expansion and containment validation between config and
  directory resources.
- [ ] Make resource clean-plan construction report declaration/discovery
  failures rather than suppressing them.
- [ ] Extend `doctor` with executable-link and directory diagnostics.
- [ ] Centralize managed-state list keys in `internal/state`.
- [ ] Split package sync, clean, and plan rendering from
  `internal/reconcile/reconcile.go`.

Do not introduce a generic resource abstraction that erases resource-specific
safety rules. Prefer a shared orchestration registry and focused resource
modules.
