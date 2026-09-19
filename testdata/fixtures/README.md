# Compatibility fixtures

`../dotfiles-packages.yaml` is the current base manifest copied from the
dotfiles repository. The files in this directory exercise the remaining state
and overlay combinations used by the package stage:

- `hosts/laptop.yaml` — a host overlay adding one desktop package.
- `legacy/` — the pre-migration package state directory.
- `shared-state.yaml` — a current state file containing resource-stage data
  that package reconciliation must preserve.
- `selected-state.yaml` — all desktop selections enabled.
- `unselected-state.yaml` — all optional desktop selections disabled.
