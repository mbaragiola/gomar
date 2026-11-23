#!/usr/bin/env bash
# Offline synthetic QA. Requires a non-root user and a local PostgreSQL toolchain.
set -euo pipefail
umask 077
repo="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
python="${QA_PYTHON:-python3}"
postgres_bin="${QA_PG_BIN:-$(pg_config --bindir)}"
chrome="${QA_CHROME:-$(command -v google-chrome || command -v chromium)}"
command -v bwrap >/dev/null
"$python" -c 'import playwright.sync_api'
[[ -x "$postgres_bin/initdb" && -x "$chrome" && "$(id -u)" != 0 ]]
export GOPROXY=off
bash "$repo/scripts/stage-web.sh"
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
timeout 150s bwrap --die-with-parent --unshare-net --bind / / --dev-bind /dev /dev --proc /proc \
  "$python" "$repo/scripts/qa/mcp_stock_journey.py" "$tmp" "$postgres_bin" "$chrome"
