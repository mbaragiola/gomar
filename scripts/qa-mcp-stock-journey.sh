#!/usr/bin/env bash
# Offline synthetic QA. Requires a non-root user and a local PostgreSQL toolchain.
set -euo pipefail
umask 077
with_local_checks=false
with_codex=false
usage() {
  printf 'Usage: %s [--with-local-checks] [--with-installed-codex]\n' "$0" >&2
  exit 2
}
while (( $# )); do
  case "$1" in
    --with-local-checks)
      "$with_local_checks" && usage
      with_local_checks=true ;;
    --with-installed-codex)
      "$with_codex" && usage
      with_codex=true ;;
    *) usage ;;
  esac
  shift
done
repo="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
python="${QA_PYTHON:-python3}"
postgres_bin="${QA_PG_BIN:-$(pg_config --bindir)}"
chrome="${QA_CHROME:-$(command -v google-chrome || command -v chromium)}"
command -v bwrap >/dev/null
"$python" -c 'import playwright.sync_api'
[[ -x "$postgres_bin/initdb" && -x "$chrome" && "$(id -u)" != 0 ]]
codex=()
if "$with_codex"; then
  if ! codex_path="$(command -v "${QA_CODEX:-codex}")" || [[ ! -x "$codex_path" ]]; then
    printf 'Installed Codex requested but not found. Set QA_CODEX to its executable.\n' >&2
    exit 1
  fi
  codex=("$(realpath "$codex_path")")
fi
export GOPROXY=off
bash "$repo/scripts/stage-web.sh"
isolated=(bwrap --die-with-parent --unshare-net --bind / / --dev-bind /dev /dev --proc /proc)
if "$with_local_checks"; then
  # Both the app tests and journey use the freshly staged web assets above.
  # Loopback HTTP fixtures work, but checks cannot connect to external services.
  "${isolated[@]}" bash -euo pipefail -c '
    repo=$1
    unset GOMAR_LOCAL_POSTGRES_TEST_DSN GOMAR_POSTGRES_DSN
    npm --prefix "$repo/frontend" run lint
    npm --prefix "$repo/frontend" test
    for module in "$repo" "$repo/app"; do
      go -C "$module" test -count=1 ./...
      go -C "$module" vet ./...
    done
  ' _ "$repo"
  # Ensure the browser binary embeds the stage verified by the Go checks.
  cmp "$repo/var/static/index.html" "$repo/app/frontendembed/web/index.html"
  diff -qr "$repo/var/static" "$repo/app/frontendembed/web/static"
fi
tmp="$(mktemp -d /tmp/gomar-stock-qa.XXXXXXXX)"
cleanup() {
  "$postgres_bin/pg_ctl" -D "$tmp/db" -m fast -w stop >/dev/null 2>&1 || true
  rm -rf -- "$tmp"
}
trap cleanup EXIT
go -C "$repo/app" build -buildvcs=false -o "$tmp/gomar" .
go -C "$repo/app" build -buildvcs=false -o "$tmp/gomar-mcp" ./cmd/gomar-mcp
# initdb, Chrome, the app, and MCP run with no external network. PostgreSQL
# listens only on a private Unix socket, so cleanup can stop it outside bwrap.
timeout 150s "${isolated[@]}" \
  "$python" -B "$repo/scripts/qa/mcp_stock_journey.py" "$tmp" "$postgres_bin" "$chrome" "${codex[@]}"
