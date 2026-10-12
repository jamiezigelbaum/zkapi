#!/usr/bin/env bash
# Test fixtures only. Never install these in a managed/release bundle.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
clientd="$(cd "$here/../../.." && pwd)"
out=${1:?Usage: qualification/build.sh ABSOLUTE_OUTPUT_DIR}
[[ "$out" = /* ]] || { echo 'Output directory must be absolute' >&2; exit 1; }
export GOTOOLCHAIN="$(sed -n 's/^GO_VERSION="\([^"]*\)".*/\1/p' "$clientd/olympus/build.sh")"
export GOOS=linux GOARCH=amd64 CGO_ENABLED=0 GOFLAGS=-mod=readonly
mkdir -p "$out"
cd "$clientd"
go build -trimpath -buildvcs=false -tags=qualification -o "$out/zkapi-confine-ablation" ./cmd/zkapi-confine
go build -trimpath -buildvcs=false -tags=qualification,qualification_adversary -o "$out/zkapi-confine-init" ./cmd/zkapi-confine
go build -trimpath -buildvcs=false -tags= -o "$out/gostandin" ./cmd/zkapi-confine/qualification/gostandin
