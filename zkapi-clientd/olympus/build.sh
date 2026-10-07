#!/usr/bin/env bash
# Reproducible darwin/arm64 build of the Olympus zkapi-clientd fork.
# Output: dist/zkapi-clientd, dist/SHA256SUMS, dist/third-party/ (Go module notices).
# Builds twice (once in place, once from a clean `git archive` copy in a temp
# dir) and FAILS if the two binaries differ.
set -euo pipefail

GO_VERSION="go1.27.1"                  # pinned toolchain
VERSION="0.1.6-olympus1"               # upstream clientd-v0.1.6 + Olympus patch
HERE="$(cd "$(dirname "$0")" && pwd)"
CLIENTD="$(cd "$HERE/.." && pwd)"
REPO="$(git -C "$CLIENTD" rev-parse --show-toplevel)"
SUBDIR="$(realpath --relative-to="$REPO" "$CLIENTD" 2>/dev/null || python3 -c 'import os,sys;print(os.path.relpath(sys.argv[1],sys.argv[2]))' "$CLIENTD" "$REPO")"
DIST="$HERE/dist"

export GOTOOLCHAIN="$GO_VERSION"
export GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 GOFLAGS=-mod=readonly

if [ "$(go env GOVERSION)" != "$GO_VERSION" ]; then
  echo "build.sh: expected $GO_VERSION, got $(go env GOVERSION)" >&2; exit 1
fi

build() { # build <module-dir> <output>
  ( cd "$1" && go build -trimpath -buildvcs=false \
      -ldflags="-s -w -X main.version=$VERSION" -o "$2" ./cmd/zkapi-clientd )
}
sha() { shasum -a 256 "$1" | awk '{print $1}'; }

rm -rf "$DIST"; mkdir -p "$DIST"
build "$CLIENTD" "$DIST/zkapi-clientd"
FIRST="$(sha "$DIST/zkapi-clientd")"

TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
git -C "$REPO" archive --format=tar HEAD "$SUBDIR" | tar -x -C "$TMP"
build "$TMP/$SUBDIR" "$TMP/second.bin"
SECOND="$(sha "$TMP/second.bin")"

if [ "$FIRST" != "$SECOND" ]; then
  echo "build.sh: NOT REPRODUCIBLE: $FIRST != $SECOND" >&2; exit 1
fi
( cd "$DIST" && shasum -a 256 zkapi-clientd > SHA256SUMS )

# Licence texts for every Go module linked into the binary (Go-only; the
# companion's notices come from upstream's release tarball).
( cd "$CLIENTD" && go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}} {{.Dir}}{{end}}{{end}}' ./cmd/zkapi-clientd ) | sort -u |
while read -r mod ver dir; do
  dest="$DIST/third-party/$(echo "$mod@$ver" | tr '/' '_')"
  for f in "$dir"/*; do
    case "$(basename "$f" | tr a-z A-Z)" in
      LICENSE*|LICENCE*|COPYING*|NOTICE*|AUTHORS*|PATENTS*) mkdir -p "$dest"; cp "$f" "$dest/";;
    esac
  done
done
( cd "$CLIENTD" && go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}}{{end}}{{end}}' ./cmd/zkapi-clientd | sort -u ) > "$DIST/third-party/MODULES.txt"
if [ -f "$(go env GOROOT)/LICENSE" ]; then cp "$(go env GOROOT)/LICENSE" "$DIST/third-party/Go-LICENSE"
else echo "Go standard library: BSD-3-Clause, https://go.dev/LICENSE (toolchain $GO_VERSION; no LICENSE file in this GOROOT)" > "$DIST/third-party/Go-LICENSE.txt"; fi

echo "reproducible: yes ($GO_VERSION, darwin/arm64, -trimpath)"
cat "$DIST/SHA256SUMS"
