#!/usr/bin/env bash
# Offline synthetic QA. Requires a non-root user and a local PostgreSQL toolchain.
set -euo pipefail
umask 077
with_local_checks=false
with_codex=false
with_purchases=false
with_bookings_wiki=false
with_barcode=false
with_pos_prebill=false
with_pos_table_checks=false
usage() {
  printf 'Usage: %s [--with-local-checks] [--with-installed-codex] [--with-purchases] [--with-bookings-wiki] [--with-pos-prebill] [--with-barcode] [--with-pos-table-checks]\n' "$0" >&2
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
    --with-purchases)
      "$with_purchases" && usage
      with_purchases=true ;;
    --with-bookings-wiki)
      "$with_bookings_wiki" && usage
      with_bookings_wiki=true ;;
    --with-pos-prebill)
      "$with_pos_prebill" && usage
      with_pos_prebill=true ;;
    --with-barcode)
      "$with_barcode" && usage
      with_barcode=true ;;
    --with-pos-table-checks)
      "$with_pos_table_checks" && usage
      with_pos_table_checks=true ;;
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
if "$with_pos_prebill"; then command -v pdftotext >/dev/null; fi
codex=""
if "$with_codex"; then
  if ! codex_path="$(command -v "${QA_CODEX:-codex}")" || [[ ! -x "$codex_path" ]]; then
    printf 'Installed Codex requested but not found. Set QA_CODEX to its executable.\n' >&2
    exit 1
  fi
  codex="$(realpath "$codex_path")"
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
  if [[ -s "$tmp/db/postmaster.pid" ]]; then
    if ! "$postgres_bin/pg_ctl" -D "$tmp/db" -m fast -w stop >/dev/null 2>&1 ||
      [[ -e "$tmp/db/postmaster.pid" ]]; then
      printf 'Synthetic PostgreSQL shutdown failed; no QA pass report.\n' >&2
      return 1
    fi
  fi
  rm -rf -- "$tmp"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
go -C "$repo/app" build -buildvcs=false -o "$tmp/gomar" .
go -C "$repo/app" build -buildvcs=false -o "$tmp/gomar-mcp" ./cmd/gomar-mcp
# initdb, Chrome, the app, and MCP run with no external network. PostgreSQL
# listens only on a private Unix socket, so cleanup can stop it outside bwrap.
timeout 150s "${isolated[@]}" \
  "$python" -B "$repo/scripts/qa/mcp_stock_journey.py" "$tmp" "$postgres_bin" "$chrome" "$codex" "$with_purchases" "$with_bookings_wiki" "$with_pos_prebill" "$with_barcode" "$with_pos_table_checks" >"$tmp/report.log"
report="$(cat "$tmp/report.log")"
cleanup
trap - EXIT
printf '%s\n' "$report"
