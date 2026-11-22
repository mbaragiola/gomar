#!/usr/bin/env bash
set -euo pipefail
repo="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
frontend="$repo/frontend"
embed="$repo/app/frontendembed"
if [[ ! -d "$frontend/node_modules" ]]; then
  printf '%s\n' 'Install the pinned frontend dependencies with npm ci --prefix frontend first.' >&2
  exit 1
fi
npm --prefix "$frontend" run build
dist="$repo/var/static"
for name in index.html app.js style.css pos-shell.html pos-sw.js; do
  if [[ ! -s "$dist/$name" ]]; then
    printf 'Missing Vite output: %s\n' "$name" >&2
    exit 1
  fi
done
scratch="$(mktemp -d "$embed/.stage.XXXXXXXX")"
trap 'rm -rf -- "$scratch"' EXIT
mkdir "$scratch/web" "$scratch/web/static"
cp -- "$embed/shell.html" "$scratch/web/index.html"
cp -R -- "$dist/." "$scratch/web/static/"
if [[ -e "$embed/web" || -L "$embed/web" ]]; then
  mv -- "$embed/web" "$scratch/previous"
fi
if ! mv -- "$scratch/web" "$embed/web"; then
  if [[ -e "$scratch/previous" || -L "$scratch/previous" ]]; then
    mv -- "$scratch/previous" "$embed/web"
  fi
  exit 1
fi
