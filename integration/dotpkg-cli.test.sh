#!/usr/bin/env bash

set -Eeuo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
temporary="$(mktemp -d)"
trap 'rm -rf "$temporary"' EXIT

mkdir -p "$temporary/bin" "$temporary/installed" "$temporary/home" "$temporary/manifest-root/config"
go build -o "$temporary/dotpkg" ./cmd/dotpkg
printf 'tool configuration\n' > "$temporary/manifest-root/config/tool"

cat > "$temporary/bin/pacman" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
case "${1:-}" in
  -Q)
    [ -f "$FAKE_INSTALLED/$2" ]
    ;;
  -Slq)
    printf 'git\ncurl\n'
    ;;
  -S)
    if [ "${PACMAN_FAIL:-0}" = 1 ]; then
      exit 42
    fi
    for package in "${@:4}"; do
      touch "$FAKE_INSTALLED/$package"
    done
    ;;
  *)
    printf 'unexpected pacman arguments: %s\n' "$*" >&2
    exit 2
    ;;
esac
EOF
cat > "$temporary/bin/sudo" <<'EOF'
#!/usr/bin/env bash
exec "$@"
EOF
cat > "$temporary/bin/id" <<'EOF'
#!/usr/bin/env bash
[ "${1:-}" = -nG ] && exit 0
exit 2
EOF
chmod +x "$temporary/bin"/*

cat > "$temporary/manifest-root/packages.yaml" <<'EOF'
source: repo
common:
  packages:
    headless:
      git: {}
profiles:
  desktop:
    packages:
      desktop: {}
  server:
    packages:
      headless: {}
resources:
  configs:
    - source: config/tool
      target: $XDG_CONFIG_HOME/tool
EOF

export PATH="$temporary/bin:$PATH"
export HOME="$temporary/home"
export XDG_CONFIG_HOME="$HOME/.config"
export FAKE_INSTALLED="$temporary/installed"
export XDG_STATE_HOME="$temporary/state"
manifest="$temporary/manifest-root/packages.yaml"
state="$temporary/state/dotpkg/state.yaml"

plan="$($temporary/dotpkg plan --manifest "$manifest" --state-file "$state" --profile server --resources --root "$temporary/manifest-root" --output json)"
grep -q '"stage":"packages"' <<<"$plan"

"$temporary/dotpkg" sync --manifest "$manifest" --state-file "$state" --profile server --resources --root "$temporary/manifest-root" --yes >/dev/null
[ -f "$temporary/installed/git" ]
[ "$(readlink "$HOME/.config/tool")" = "$temporary/manifest-root/config/tool" ]
grep -q -- '- git$' "$state"

doctor="$($temporary/dotpkg doctor --manifest "$manifest" --state-file "$state" --profile server --root "$temporary/manifest-root" --output json)"
grep -q '"ok":true' <<<"$doctor"

rm "$HOME/.config/tool"
set +e
doctor="$($temporary/dotpkg doctor --manifest "$manifest" --state-file "$state" --profile server --root "$temporary/manifest-root" 2>&1)"
doctor_status=$?
set -e
[ "$doctor_status" -eq 1 ]
grep -q 'config link is missing' <<<"$doctor"

export PACMAN_FAIL=1
sed -i '/git: {}/a\      curl: {}' "$manifest"
set +e
"$temporary/dotpkg" sync --manifest "$manifest" --state-file "$state" --profile server --yes >/dev/null 2>&1
sync_status=$?
set -e
[ "$sync_status" -ne 0 ]
grep -q -- '- git$' "$state"
! grep -q -- '- curl$' "$state"

printf 'dotpkg CLI integration: ok\n'
