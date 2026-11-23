#!/usr/bin/env bash
# Run only the synthetic fiscal outbox test in a new disposable database.
set -euo pipefail
umask 077
repo="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
[[ $# = 0 && $(id -u) != 0 ]]
unset GOMAR_FISCAL_OUTBOX_TEST_DSN PGSERVICE PGSERVICEFILE PGOPTIONS PGHOST PGPORT PGUSER PGPASSWORD PGDATABASE
export GOPROXY=off
pg="$(realpath "${QA_PG_BIN:-$(pg_config --bindir)}")"
for tool in initdb pg_ctl createdb; do [[ -x "$pg/$tool" ]]; done
# Keep a short private path. Fedora's shared /tmp may be out of inodes.
scratch="$(mktemp -d "${QA_OUTBOX_TMPDIR:-/tmp}/gfq.XXXXXXXX")"
cleanup() {
    if [[ -s "$scratch/data/postmaster.pid" ]]; then
        "$pg/pg_ctl" -D "$scratch/data" -m fast -w stop >/dev/null || return 1
        [[ ! -e "$scratch/data/postmaster.pid" ]] || return 1
    fi
    rm -rf -- "$scratch"
}
trap cleanup EXIT
mkdir -m 700 "$scratch/s" "$scratch/tmp"
export TMPDIR="$scratch/tmp" GOTMPDIR="$scratch/tmp"
timeout 150s bwrap --die-with-parent --unshare-net --bind / / --dev-bind /dev /dev --proc /proc \
    bash -euo pipefail -s -- "$repo" "$scratch" "$pg" <<'INNER'
repo=$1 root=$2 pg=$3
cleanup() {
    if [[ -s "$root/data/postmaster.pid" ]]; then
        "$pg/pg_ctl" -D "$root/data" -m fast -w stop >/dev/null
        [[ ! -e "$root/data/postmaster.pid" ]]
    fi
}
trap cleanup EXIT
"$pg/initdb" -D "$root/data" --locale=C --encoding=UTF8 --auth=trust >/dev/null
"$pg/pg_ctl" -D "$root/data" -l "$root/server.log" -w start \
    -o "-k $root/s -p 55590 -c listen_addresses=''" >/dev/null
"$pg/createdb" -h "$root/s" -p 55590 gomar_qa_fiscal
export GOMAR_FISCAL_OUTBOX_TEST_DSN="host=$root/s port=55590 dbname=gomar_qa_fiscal sslmode=disable"
go -C "$repo/app" test -v -mod=readonly -count=1 -race -run '^TestSyntheticOutboxPostgres$' ./internal/tenantfiscalpreview
cleanup
trap - EXIT
INNER
cleanup
trap - EXIT
