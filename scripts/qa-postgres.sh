#!/usr/bin/env bash
# Opt-in Gomar database checks. Never use a pre-existing database or socket.
set -euo pipefail
umask 077
(( $# == 0 )) || { printf 'Usage: %s\n' "$0" >&2; exit 2; }
repo="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "$repo"
python="$(realpath "$(command -v "${QA_PYTHON:-/usr/bin/python3}")")"
pg="$(realpath "${QA_PG_BIN:-$(pg_config --bindir)}")"
bwrap="$(command -v bwrap)"
go="$(command -v go)"
[[ $(id -u) != 0 ]]
for name in initdb pg_ctl createdb dropdb; do
  [[ -x "$pg/$name" ]]
done
[[ -d "$repo/frontend/node_modules" ]] || {
  printf '%s\n' 'Install the pinned frontend dependencies with npm ci --prefix frontend first.' >&2
  exit 1
}

# Do not read or stage untracked data to calculate a source identity.
clean_source() {
  [[ -z "$(git status --porcelain=v1 --untracked-files=normal)" ]]
}
if ! clean_source; then
  printf 'Run PostgreSQL QA in a clean worktree.\n' >&2
  exit 1
fi
commit="$(git rev-parse HEAD)"
tree="$(git rev-parse 'HEAD^{tree}')"
scratch="$(mktemp -d /tmp/gomar-postgres-qa.XXXXXXXX)"
cleanup() {
  if [[ -s "$scratch/data/postmaster.pid" ]] &&
    ! "$pg/pg_ctl" -D "$scratch/data" -m fast -w stop >/dev/null 2>&1; then
    printf 'Disposable PostgreSQL fallback shutdown failed; no pass manifest.\n' >&2
    return 1
  fi
  rm -rf -- "$scratch"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Compile tracked source from the selected commit, not ignored overlays.
# These installed frontend dependencies are snapshotted for the fresh build.
source="$scratch/source"
mkdir "$source"
git archive "$commit" | tar -x -C "$source"
cp -a --reflink=auto -- "$repo/frontend/node_modules" "$source/frontend/"
build=("$bwrap" --die-with-parent --unshare-net --bind / / --dev-bind /dev /dev --proc /proc)
"${build[@]}" bash "$source/scripts/stage-web.sh"
cmp "$source/var/static/index.html" "$source/app/frontendembed/web/index.html"
diff -qr "$source/var/static" "$source/app/frontendembed/web/static"

unset GOFLAGS GOROOT GOTOOLCHAIN GOMODCACHE GONOSUMDB GONOPROXY GOPRIVATE GOSUMDB
export GOENV=off GOWORK=off GOPROXY=off GOTOOLCHAIN=auto
# Select the app's verified cached toolchain with normal checksum policy,
# then pin that executable; builds cannot fetch a different toolchain.
goroot="$("${build[@]}" "$go" -C "$source/app" env GOROOT)"
go="$goroot/bin/go"
[[ -x "$go" ]]
export GOTOOLCHAIN=local
"${build[@]}" "$go" -C "$source/app" build -mod=readonly -o "$scratch/test2json" cmd/test2json
"${build[@]}" "$go" -C "$source/app" test -mod=readonly -c -o "$scratch/app-tests" .

# A fresh root in the namespace is owned by this user. With the host root
# bind, unprivileged Bubblewrap maps host root to nobody and Gomar rightly
# refuses that Unix-socket ancestry. Only the fresh fixture is writable.
runtime=("$bwrap" --die-with-parent --unshare-net --tmpfs / --ro-bind /usr /usr
  --symlink usr/bin /bin --symlink usr/lib /lib --symlink usr/lib64 /lib64
  --ro-bind /etc/passwd /etc/passwd --ro-bind /etc/group /etc/group
  --dev /dev --proc /proc --dir /tmp --bind "$scratch" /fixture --chdir /fixture)
if [[ "$pg" != /usr/* ]]; then
  runtime+=(--ro-bind "$(dirname "$pg")" "$(dirname "$pg")")
fi
if [[ "$python" != /usr/* ]]; then
  runtime+=(--ro-bind "$(dirname "$(dirname "$python")")" "$(dirname "$(dirname "$python")")")
fi
timeout 150s "${runtime[@]}" "$python" -I /fixture/source/scripts/qa/postgres_suite.py \
  /fixture "$pg" /fixture/test2json "$commit" "$tree"
if [[ "$(git rev-parse HEAD)" != "$commit" || "$(git rev-parse 'HEAD^{tree}')" != "$tree" ]] || ! clean_source; then
  printf 'Gomar source changed during QA; no pass manifest was emitted.\n' >&2
  exit 1
fi
# Python has already confirmed shutdown. Keep only the nonsecret summary
# in memory so cleanup succeeds before this shell emits the pass marker.
summary="$(cat "$scratch/manifest.json")"
cleanup
trap - EXIT
printf '%s\n' "$summary"
