#!/usr/bin/env bash
set -euo pipefail
repo="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
bash "$repo/scripts/stage-web.sh"
mkdir -p "$repo/bin"
(cd "$repo/app" && go build -mod=readonly -trimpath -buildvcs=false -o "$repo/bin/gomar-server" .)
(cd "$repo/app" && go build -mod=readonly -trimpath -buildvcs=false -o "$repo/bin/gomar-mcp" ./cmd/gomar-mcp)
