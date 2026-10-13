#!/usr/bin/env bash
# Reproducible build of the Olympus zkapi-clientd fork.
#
#   build.sh                 # darwin/arm64 (the historical default)
#   build.sh linux-amd64     # linux/amd64 daemon and confinement launcher
#
# Output: dist/zkapi-clientd, dist/SHA256SUMS, dist/third-party/ (Go module
# notices), plus dist/bin/zkapi-confine and dist/reproducibility.json for
# linux/amd64.
# Builds twice (once in place, once from a clean `git archive` copy in a temp
# dir) and FAILS if the two binaries differ.
set -euo pipefail

GO_VERSION="go1.27.1"                  # pinned toolchain
VERSION="0.1.6-olympus4"               # upstream clientd-v0.1.6 + Olympus patches
target="${1:-darwin-arm64}"
HERE="$(cd "$(dirname "$0")" && pwd)"
CLIENTD="$(cd "$HERE/.." && pwd)"
REPO="$(git -C "$CLIENTD" rev-parse --show-toplevel)"
SUBDIR="$(realpath --relative-to="$REPO" "$CLIENTD" 2>/dev/null || python3 -c 'import os,sys;print(os.path.relpath(sys.argv[1],sys.argv[2]))' "$CLIENTD" "$REPO")"
DIST="$HERE/dist"

case "$target" in
  darwin-arm64) goos=darwin; goarch=arm64 ;;
  linux-amd64) goos=linux; goarch=amd64 ;;
  *) echo "build.sh: unsupported target $target" >&2; exit 1 ;;
esac

export GOTOOLCHAIN="$GO_VERSION"
export GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 GOFLAGS=-mod=readonly
if [ "$target" = linux-amd64 ]; then export GOAMD64=v1; fi

if [ "$(go env GOVERSION)" != "$GO_VERSION" ]; then
  echo "build.sh: expected $GO_VERSION, got $(go env GOVERSION)" >&2; exit 1
fi

build_daemon() { # build_daemon <module-dir> <output>
  ( cd "$1" && go build -trimpath -buildvcs=false \
      -ldflags="-s -w -X main.version=$VERSION" -o "$2" ./cmd/zkapi-clientd )
}
build_confine() { # build_confine <module-dir> <output>
  ( cd "$1" && go build -trimpath -buildvcs=false -tags= \
      -ldflags="-s -w -X main.version=$VERSION" -o "$2" ./cmd/zkapi-confine )
}
# shasum ships with macOS and Debian's perl; Arch/Omarchy has only sha256sum.
if command -v sha256sum >/dev/null 2>&1; then sha256() { sha256sum "$@"; }; else sha256() { shasum -a 256 "$@"; }; fi
sha() { sha256 "$1" | awk '{print $1}'; }

rm -rf "$DIST"; mkdir -p "$DIST"
build_daemon "$CLIENTD" "$DIST/zkapi-clientd"
FIRST_DAEMON="$(sha "$DIST/zkapi-clientd")"
if [ "$target" = linux-amd64 ]; then
  mkdir -p "$DIST/bin"
  build_confine "$CLIENTD" "$DIST/bin/zkapi-confine"
  FIRST_CONFINE="$(sha "$DIST/bin/zkapi-confine")"
fi

TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
git -C "$REPO" archive --format=tar HEAD "$SUBDIR" | tar -x -C "$TMP"
build_daemon "$TMP/$SUBDIR" "$TMP/second-daemon.bin"
SECOND_DAEMON="$(sha "$TMP/second-daemon.bin")"

if [ "$FIRST_DAEMON" != "$SECOND_DAEMON" ]; then
  echo "build.sh: zkapi-clientd NOT REPRODUCIBLE: $FIRST_DAEMON != $SECOND_DAEMON" >&2; exit 1
fi
if [ "$target" = linux-amd64 ]; then
  build_confine "$TMP/$SUBDIR" "$TMP/second-confine.bin"
  SECOND_CONFINE="$(sha "$TMP/second-confine.bin")"
  if [ "$FIRST_CONFINE" != "$SECOND_CONFINE" ]; then
    echo "build.sh: zkapi-confine NOT REPRODUCIBLE: $FIRST_CONFINE != $SECOND_CONFINE" >&2; exit 1
  fi
  ( cd "$DIST" && sha256 zkapi-clientd bin/zkapi-confine > SHA256SUMS )

  commit="$(git -C "$REPO" rev-parse HEAD)"
  python3 - "$DIST/reproducibility.json" "$VERSION" "$GO_VERSION" "$commit" \
    "$FIRST_DAEMON" "$SECOND_DAEMON" "$FIRST_CONFINE" "$SECOND_CONFINE" <<'PY'
import json
import platform
import subprocess
import sys

path, version, toolchain, commit, first_daemon, second_daemon, first_confine, second_confine = sys.argv[1:]
go_env_fields = [
    "GOVERSION",
    "GOOS",
    "GOARCH",
    "GOAMD64",
    "CGO_ENABLED",
    "GOFLAGS",
    "GOEXPERIMENT",
    "GOTOOLCHAIN",
    "GOHOSTOS",
    "GOHOSTARCH",
    "GOROOT",
]
manifest = {
    "artifacts": {
        "zkapi-clientd": {
            "working_tree_sha256": first_daemon,
            "git_archive_head_sha256": second_daemon,
        },
        "bin/zkapi-confine": {
            "working_tree_sha256": first_confine,
            "git_archive_head_sha256": second_confine,
        },
    },
    "build_flags": {
        "zkapi-clientd": [
            "-trimpath",
            "-buildvcs=false",
            f"-ldflags=-s -w -X main.version={version}",
        ],
        "zkapi-confine": [
            "-trimpath",
            "-buildvcs=false",
            "-tags=",
            f"-ldflags=-s -w -X main.version={version}",
        ],
    },
    "environment": {
        "go_env": json.loads(subprocess.check_output(["go", "env", "-json", *go_env_fields], text=True)),
        "kernel": {key: value for key, value in platform.uname()._asdict().items() if key != "node"},
    },
    "go_toolchain": toolchain,
    "source_commit": commit,
    "target": "linux-amd64",
    "version": version,
}
with open(path, "w", encoding="utf-8") as output:
    json.dump(manifest, output, indent=2, sort_keys=True)
    output.write("\n")
PY
else
  ( cd "$DIST" && sha256 zkapi-clientd > SHA256SUMS )
fi

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

echo "reproducible: yes ($GO_VERSION, $goos/$goarch, -trimpath)"
cat "$DIST/SHA256SUMS"
