#!/usr/bin/env bash
# Opt-in Gomar database checks. Never use a pre-existing database or socket.
set -euo pipefail
umask 077
restore=false
case "$#:${1:-}" in
  0:) ;;
  1:--private-restore) restore=true ;;
  *) printf 'Usage: %s [--private-restore]\n' "$0" >&2; exit 2 ;;
esac
repo="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "$repo"
# Keep relative PATH entries anchored to the worktree, not the scratch parent.
# Do not resolve executable symlinks: a toolchain shim may use its own name.
path_entries="$PATH:"
PATH=
while [[ -n "$path_entries" ]]; do
  entry="${path_entries%%:*}"
  path_entries="${path_entries#*:}"
  [[ "$entry" == /* ]] || entry="$repo${entry:+/$entry}"
  PATH="${PATH:+$PATH:}$entry"
done
python="$(realpath "$(command -v "${QA_PYTHON:-/usr/bin/python3}")")"
pg="$(realpath "${QA_PG_BIN:-$(pg_config --bindir)}")"
bwrap="$(command -v bwrap)"
go="$(command -v go)"
uid="$(id -u)"
[[ "$uid" != 0 ]]
pg_tools=(initdb pg_ctl createdb dropdb)
if "$restore"; then pg_tools+=(psql pg_dump pg_restore); fi
for name in "${pg_tools[@]}"; do
  [[ -x "$pg/$name" ]]
done
[[ -d "$repo/frontend/node_modules" ]] || {
  printf '%s\n' 'Install the pinned frontend dependencies with npm ci --prefix frontend first.' >&2
  exit 1
}

# Do not read or stage untracked data to calculate a source identity.
clean_source() {
  [[ -z "$(git -C "$repo" status --porcelain=v1 --untracked-files=normal)" ]]
}
if ! clean_source; then
  printf 'Run PostgreSQL QA in a clean worktree.\n' >&2
  exit 1
fi
commit="$(git rev-parse HEAD)"
tree="$(git rev-parse 'HEAD^{tree}')"
scratch_parent=/tmp
if [[ -v QA_POSTGRES_SCRATCH_PARENT ]]; then
  scratch_parent="$QA_POSTGRES_SCRATCH_PARENT"
  invalid_parent() {
    printf '%s\n' 'QA_POSTGRES_SCRATCH_PARENT must be an absolute, canonical, owner-only (0700) directory outside the worktree with safe parent directories.' >&2
    exit 2
  }
  [[ "$scratch_parent" == /* && "$scratch_parent" != / &&
    "$scratch_parent" != *[[:cntrl:]]* && -d "$scratch_parent" &&
    "$scratch_parent" == "$(realpath -e -- "$scratch_parent")" &&
    "$scratch_parent" != "$repo" && "$scratch_parent" != "$repo/"* ]] || invalid_parent

  # A different user must not be able to replace any part of the path.
  ancestor=
  IFS=/ read -ra parts <<< "${scratch_parent#/}"
  for part in "${parts[@]}"; do
    ancestor+="/$part"
    [[ -d "$ancestor" && ! -L "$ancestor" ]] || invalid_parent
    metadata="$(stat -c '%u %a' -- "$ancestor")" || invalid_parent
    read -r owner mode <<< "$metadata"
    [[ "$owner" == "$uid" || "$owner" == 0 ]] || invalid_parent
    # A trusted sticky parent (such as /var/tmp) protects its owned entries.
    (( !(8#$mode & 0022) || (8#$mode & 01000) )) || invalid_parent
  done
  [[ "$owner" == "$uid" && "$mode" == 700 ]] || invalid_parent
fi

# Anchor cleanup to this directory. Only remove the fresh child created here,
# never the configured parent, even if its pathname is moved during the run.
cd -- "$scratch_parent"
scratch_name="$(mktemp -d -- gomar-postgres-qa.XXXXXXXX)"
scratch="$scratch_parent/$scratch_name"
scratch_identity="$(stat -c '%d:%i' -- "$scratch_name")"
cleanup() {
  if [[ ! -d "$scratch_name" || -L "$scratch_name" ||
    "$(stat -c '%d:%i' -- "$scratch_name")" != "$scratch_identity" ]]; then
    printf '%s\n' 'Disposable PostgreSQL scratch directory changed; refusing cleanup. No pass manifest.' >&2
    return 1
  fi
  if [[ -s "$scratch_name/data/postmaster.pid" ]] &&
    ! "$pg/pg_ctl" -D "$scratch_name/data" -m fast -w stop >/dev/null 2>&1; then
    printf 'Disposable PostgreSQL fallback shutdown failed; no pass manifest.\n' >&2
    return 1
  fi
  rm -rf -- "$scratch_name"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Compile tracked source from the selected commit, not ignored overlays.
# These installed frontend dependencies are snapshotted for the fresh build.
source="$scratch/source"
mkdir "$source"
git -C "$repo" archive "$commit" | tar -x -C "$source"
cp -a --reflink=auto -- "$repo/frontend/node_modules" "$source/frontend/"
if [[ -v QA_POSTGRES_SCRATCH_PARENT ]]; then
  mkdir "$scratch/build-tmp"
  export TMPDIR="$scratch/build-tmp" GOTMPDIR="$scratch/build-tmp"
fi
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
if "$restore"; then
  "${build[@]}" "$go" -C "$source/app" build -buildvcs=false -mod=readonly -o "$scratch/gomar" .
else
  "${build[@]}" "$go" -C "$source/app" build -mod=readonly -o "$scratch/test2json" cmd/test2json
  "${build[@]}" "$go" -C "$source/app" test -mod=readonly -c -o "$scratch/app-tests" .
fi

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
if "$restore"; then
  args=("$python" -I /fixture/source/scripts/qa/private_restore.py)
  timeout 60s "${runtime[@]}" "${args[@]}" seal "$pg" "$commit" "$tree"
  # The Go app still expects 0700 directories and 0600 files. Enforce
  # read-only file access at the mount, not by changing these modes.
  timeout 60s "${runtime[@]}" \
    --ro-bind "$scratch/pair" /fixture/pair \
    --ro-bind "$scratch/restored-state" /fixture/restored-state \
    "${args[@]}" prove "$pg" "$commit" "$tree"
else
  timeout 150s "${runtime[@]}" "$python" -I /fixture/source/scripts/qa/postgres_suite.py \
    /fixture "$pg" /fixture/test2json "$commit" "$tree"
fi
if [[ "$(git -C "$repo" rev-parse HEAD)" != "$commit" || "$(git -C "$repo" rev-parse 'HEAD^{tree}')" != "$tree" ]] || ! clean_source; then
  printf 'Gomar source changed during QA; no pass manifest was emitted.\n' >&2
  exit 1
fi
# Python has already confirmed shutdown. Keep only the nonsecret summary
# in memory so cleanup succeeds before this shell emits the pass marker.
summary="$(cat "$scratch/manifest.json")"
cleanup
trap - EXIT
printf '%s\n' "$summary"
