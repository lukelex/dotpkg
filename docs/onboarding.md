# Guided installation

`dotpkg wizard --manifest package.yaml --root /path/to/dotfiles` runs a
full-screen Bubble Tea frontend. The manifest owns personal onboarding; dotpkg
owns package choices, presentation, checks, progress, and recovery. No package
transaction runs before plan approval. The static Linux release includes the UI;
no Go runtime or separate frontend download is needed.

## Manifest contract (version 1)

```yaml
onboarding:
  version: 1
  title: Set up this machine
  description: Install tools and connect your accounts.
  continue_on_login: true
  inputs:
    - id: timezone
      title: Timezone
      description: Keep the current zone or enter an IANA timezone.
      default: UTC
      default_command: [timedatectl, show, --property=Timezone, --value]
  steps:
    - id: account
      title: Connect an account
      description: Verify access before using private resources.
      instructions: Open the account settings, add the displayed public key, then check again.
      url: https://example.com/settings/keys
      optional: true
      check: [my-account-helper, check]
      display: [my-account-helper, public-key]
      action: [my-account-helper, login]
      interactive: true
    - id: resources
      title: Fetch private resources
      description: Download the selected private resources.
      depends_on: [account]
      action: [my-resource-helper, fetch]
```

Onboarding fields are strictly decoded. IDs must be unique lowercase identifiers;
dependencies must reference an earlier step. Commands are argv arrays, with
environment-style expansion inside individual arguments (no shell evaluation).
`${root}`, `${manifest}`, `${profile}`, `${host}`, `${binary}`, `${selections}`,
input IDs, and environment variables are available. Use an explicit shell
script when shell logic is needed. Manifests are executable, trusted configuration,
just like installation scripts.

Inputs accept `id`, `title`, `description`, `default`, `default_command`, and
optional `choices`. Default commands and all `check` commands **must be read-only**.
Checks have a 20-second timeout; zero means ready, nonzero means setup is needed.
Their output is not logged. `display` is reserved for public information, is
bounded, and is never persisted. Do not declare secret values as inputs or display
commands. Preferences are saved as private JSON, not encrypted credential storage.

Steps additionally accept:

- `profiles`: desktop/server filter; omitted means both.
- `packages_only`: include this step in `--packages-only` runs.
- `graphical`: require an existing graphical session for the action/confirmation.
- `manual`: an explicit user acknowledgement instead of a machine check.
- `optional`: allow deferral unless an active step depends on it.
- `interactive`: release the terminal to the command, then restore the TUI.
- `log_output`: opt into output-only PTY logging for interactive installation
  commands using util-linux `script`. Leave false for account authentication.
- `unattended_action`: supported alternative to an interactive action in plain mode.
- `kind: config-conflicts`: inspect declared package/resource config links,
  individually keep or back up existing targets, and recheck before moving them.

Without a check, an installation action runs again on a later invocation.
Checks determine readiness, never a saved success flag. Manual acknowledgements
and explicit deferrals persist. Config backup rejects changed targets and retains
contents in a unique sibling `.dotpkg-backup-*` directory. Normal reconciliation
still preserves any new conflict created afterward.

## Package choices and external operations

`reconcile.SelectionChoices` returns labels, package membership, defaults, and
recorded selections. The wizard collects these before approval. Scripts receive
`DOTPKG_SELECTIONS_FILE`, an explicit JSON object:

```json
{"extras":true,"hyprland":true,"i3":false,"options.nvidia":false}
```

Pass that file through `dotpkg sync --selections PATH --require-selections`.
Unknown choices fail; explicit false values override recorded selections and
`--yes`. Unanswered choices fail under `--require-selections`. Ordinary legacy
`sync --yes` semantics are preserved. Selection changes persist only during
execution; plans can use explicit selections without writing state.

Action environments also contain `DOTPKG_BIN`, `DOTPKG_WIZARD=1`,
`INSTALL_PROFILE`, `INSTALL_HOST`, and `DOTPKG_INPUT_<UPPERCASE_ID>`.
Package-only and service-restart requests are exposed through
`DOTPKG_PACKAGES_ONLY=1` and `DOTPKG_RESTART_SERVICES=1` respectively.

During background work, Ctrl-C requests a stop at the current operation's end.
Terminal handoffs ignore SIGINT by default to avoid interrupting package writes;
external tools may implement their own cancellation. No rollback is claimed.
Failed stages remain on screen with retry, logs, and exit actions.

## Preview, plain mode, and continuation

`wizard --dry-run` lists stage descriptions without running even check/default
commands, creating directories, or acquiring locks. It works without a terminal.
`wizard --plain --yes --selections choices.json` requires explicit/recorded
component choices and input values supplied through `DOTPKG_INPUT_*` or a saved
session. Stages without an unattended implementation fail preflight rather than
waiting for invisible prompts. Plain commands have no stdin; the Arch backend
uses `sudo -n` when `DOTPKG_NONINTERACTIVE=1`.

Each manifest gets its own private session under
`$XDG_STATE_HOME/dotpkg/wizard-<manifest-path-hash>`. A lock prevents concurrent
login/manual wizards. `--resume` restores preferences and runs checks again.
During approved desktop setup, `continue_on_login` creates an XDG autostart entry
with `Terminal=true` pointing to the same binary. Install that binary at a
persistent path. The entry remains until completion/explicit deferral; the
desktop must support terminal-based XDG autostart entries. Manual resume always
remains available. No system service or login shell is modified for continuation.

`events.jsonl` exposes lifecycle events separately from human logs:

```json
{"schema":"dotpkg.wizard.event","version":1,"time":"2026-11-09T12:00:00Z","step":"packages","status":"running"}
```

Statuses include `running`, `ready`, `done`, `failed`, `deferred`, `confirmed`,
and `reviewed`. Progress reports stages and activity; package percentages are
not inferred from terminal output. `run.log` holds background output and status;
opted-in handoffs have separate `<step-id>.log` files. Account checks, displayed
public values, and account terminal output are excluded from these logs.

## Release integration

The existing static amd64/arm64 release workflow builds the frontend as part of
dotpkg. Consumers must pin a reviewed version and verify architecture-specific
checksums. A local build can be used through the consumer's `DOTPKG_BIN` override;
do not replace a release pin with an unpublished version or invented checksum.
