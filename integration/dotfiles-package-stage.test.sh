#!/usr/bin/env bash

set -Eeuo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
temporary="$(mktemp -d)"
trap 'rm -rf "$temporary"' EXIT

cat > "$temporary/fake-dotpkg" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$@" > "${DOTPKG_TEST_ARGS}"
EOF
chmod +x "$temporary/fake-dotpkg"

DOTPKG_BIN="$temporary/fake-dotpkg" \
DOTPKG_MANIFEST="$temporary/packages.yaml" \
DOTPKG_STATE="$temporary/state.yaml" \
DOTPKG_HOST=laptop \
DOTPKG_PROFILE=server \
DOTPKG_DRY_RUN=1 \
DOTPKG_YES=1 \
DOTPKG_TEST_ARGS="$temporary/args" \
  "$root/integration/dotfiles-package-stage"

mapfile -t args < "$temporary/args"
expected=(sync --manifest "$temporary/packages.yaml" --state-file "$temporary/state.yaml" --profile server --host laptop --dry-run --yes)
if [ "${args[*]}" != "${expected[*]}" ]; then
	printf 'adapter args: %s\nexpected: %s\n' "${args[*]}" "${expected[*]}" >&2
	exit 1
fi

printf 'dotfiles package adapter: ok\n'
