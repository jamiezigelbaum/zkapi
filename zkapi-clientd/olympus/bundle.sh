#!/usr/bin/env bash
# One Olympus release bundle: upstream's verified clientd-v0.1.6 archive with
# the Go daemon swapped for this fork's build. Everything else (zkapi-walletd,
# share/zkapi-clientd/proof-setup, notices) stays byte-for-byte upstream's.
#
#   bundle.sh TARGET UPSTREAM_ARCHIVE OUTPUT_DIR
#   TARGET: darwin-arm64 | darwin-amd64 | linux-amd64 | linux-arm64
#
# The daemon is pure Go (CGO_ENABLED=0), so every target cross-compiles on one
# host. It is built twice (in place, then from a clean `git archive` of HEAD)
# and the bundle is refused if the two binaries differ. Output:
# OUTPUT_DIR/zkapi-clientd_<VERSION>_<os>_<arch>.tar.gz, written by upstream's
# archive-release.py (sorted entries, root ownership, SOURCE_DATE_EPOCH times).
# linux/amd64 also writes a host-specific .build-environment.json sidecar.
set -euo pipefail
umask 022

target=${1:?Usage: bundle.sh TARGET UPSTREAM_ARCHIVE OUTPUT_DIR}
upstream=${2:?Upstream archive required}
output_dir=${3:?Output directory required}

GO_VERSION="go1.27.1"
UPSTREAM_TAG="clientd-v0.1.6"
UPSTREAM_VERSION="0.1.6"
HERE="$(cd "$(dirname "$0")" && pwd)"
CLIENTD="$(cd "$HERE/.." && pwd)"
REPO="$(git -C "$CLIENTD" rev-parse --show-toplevel)"
# One version string, owned by build.sh.
VERSION="$(sed -n 's/^VERSION="\([^"]*\)".*/\1/p' "$HERE/build.sh")"
[[ "$VERSION" =~ ^${UPSTREAM_VERSION//./\\.}-olympus[0-9]+$ ]] || { echo "bundle.sh: unexpected version '$VERSION' in build.sh" >&2; exit 1; }

case "$target" in
  darwin-arm64|darwin-amd64|linux-amd64|linux-arm64) ;;
  *) echo "bundle.sh: unsupported target $target" >&2; exit 1 ;;
esac
goos=${target%-*}
goarch=${target#*-}
sha() { python3 -c 'import hashlib,sys; print(hashlib.sha256(open(sys.argv[1],"rb").read()).hexdigest())' "$1"; }
name="zkapi-clientd_${UPSTREAM_VERSION}_${goos}_${goarch}.tar.gz"

# The upstream archive must be the one upstream's own release record pins.
want="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["artifacts"][sys.argv[2]])' \
  "$CLIENTD/packaging/releases/clientd-${UPSTREAM_VERSION}.json" "$name")"
got="$(sha "$upstream")"
[ "$want" = "$got" ] || { echo "bundle.sh: $upstream is not upstream's $name ($got != $want)" >&2; exit 1; }

export GOTOOLCHAIN="$GO_VERSION" GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 GOFLAGS=-mod=readonly
if [ "$target" = linux-amd64 ]; then export GOAMD64=v1; fi
[ "$(go env GOVERSION)" = "$GO_VERSION" ] || { echo "bundle.sh: expected $GO_VERSION, got $(go env GOVERSION)" >&2; exit 1; }

work="$(mktemp -d)"; trap 'rm -rf "$work"' EXIT
build_daemon() { ( cd "$1" && go build -trimpath -buildvcs=false -ldflags="-s -w -X main.version=$VERSION" -o "$2" ./cmd/zkapi-clientd ); }
build_confine() { ( cd "$1" && go build -trimpath -buildvcs=false -tags= -ldflags="-s -w -X main.version=$VERSION" -o "$2" ./cmd/zkapi-confine ); }

build_daemon "$CLIENTD" "$work/first-daemon"
if [ "$target" = linux-amd64 ]; then
  build_confine "$CLIENTD" "$work/first-confine"
fi
subdir="$(python3 -c 'import os,sys; print(os.path.relpath(sys.argv[1], sys.argv[2]))' "$CLIENTD" "$REPO")"
mkdir "$work/src"
git -C "$REPO" archive --format=tar HEAD "$subdir" | tar -x -C "$work/src"
build_daemon "$work/src/$subdir" "$work/second-daemon"
first_daemon="$(sha "$work/first-daemon")"
second_daemon="$(sha "$work/second-daemon")"
[ "$first_daemon" = "$second_daemon" ] || { echo "bundle.sh: zkapi-clientd NOT REPRODUCIBLE for $target ($first_daemon != $second_daemon)" >&2; exit 1; }
if [ "$target" = linux-amd64 ]; then
  build_confine "$work/src/$subdir" "$work/second-confine"
  first_confine="$(sha "$work/first-confine")"
  second_confine="$(sha "$work/second-confine")"
  [ "$first_confine" = "$second_confine" ] || { echo "bundle.sh: zkapi-confine NOT REPRODUCIBLE for $target ($first_confine != $second_confine)" >&2; exit 1; }
fi

stage="$work/stage"; mkdir "$stage"
tar -xzf "$upstream" -C "$stage"
[ "$(cat "$stage/VERSION")" = "$UPSTREAM_VERSION" ] || { echo "bundle.sh: upstream VERSION is not $UPSTREAM_VERSION" >&2; exit 1; }
for file in zkapi-clientd zkapi-walletd share/zkapi-clientd/build-info.json \
  share/zkapi-clientd/proof-setup/manifest.json share/zkapi-clientd/proof-setup/request.pk share/zkapi-clientd/proof-setup/request.vk \
  share/zkapi-clientd/proof-setup/withdrawal.pk share/zkapi-clientd/proof-setup/withdrawal.vk; do
  test -s "$stage/$file" || { echo "bundle.sh: upstream archive lacks $file" >&2; exit 1; }
done

install -m 755 "$work/first-daemon" "$stage/zkapi-clientd"
if [ "$target" = linux-amd64 ]; then
  mkdir -p "$stage/bin"
  install -m 755 "$work/first-confine" "$stage/bin/zkapi-confine"
fi
printf '%s\n' "$VERSION" > "$stage/VERSION"
cp "$HERE/NOTICE-modified.md" "$stage/NOTICE-modified.md"
commit="$(git -C "$REPO" rev-parse HEAD)"
python3 - "$stage/share/zkapi-clientd/olympus-build.json" "$target" "$VERSION" "$commit" \
  "$GO_VERSION" "$first_daemon" "$second_daemon" "${first_confine:-}" "${second_confine:-}" \
  "$UPSTREAM_TAG" "$name" "$want" <<'PY'
import json
import sys

(
    path,
    target,
    version,
    commit,
    toolchain,
    first_daemon,
    second_daemon,
    first_confine,
    second_confine,
    upstream_tag,
    upstream_archive,
    upstream_sha256,
) = sys.argv[1:]
metadata = {
    "version": version,
    "fork": "https://github.com/jamiezigelbaum/zkapi",
    "fork_commit": commit,
    "go_toolchain": toolchain,
    "zkapi_clientd_sha256": first_daemon,
    "upstream_tag": upstream_tag,
    "upstream_archive": upstream_archive,
    "upstream_archive_sha256": upstream_sha256,
    "unchanged_from_upstream": "everything except zkapi-clientd, VERSION, NOTICE-modified.md and this file",
}
if target == "linux-amd64":
    metadata["zkapi_confine_sha256"] = first_confine
    metadata["unchanged_from_upstream"] = "everything except zkapi-clientd, bin/zkapi-confine, VERSION, NOTICE-modified.md and this file"
    metadata["reproducibility"] = {
        "builds": {
            "working_tree": {
                "zkapi-clientd_sha256": first_daemon,
                "zkapi-confine_sha256": first_confine,
            },
            "git_archive_head": {
                "zkapi-clientd_sha256": second_daemon,
                "zkapi-confine_sha256": second_confine,
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
        "go_env": {
            "CGO_ENABLED": "0",
            "GOARCH": "amd64",
            "GOAMD64": "v1",
            "GOFLAGS": "-mod=readonly",
            "GOOS": "linux",
            "GOTOOLCHAIN": toolchain,
            "GOVERSION": toolchain,
        },
        "source_commit": commit,
    }
with open(path, "w", encoding="utf-8") as output:
    json.dump(metadata, output, indent=2, sort_keys=True)
    output.write("\n")
PY

mkdir -p "$output_dir"
bundle="$output_dir/zkapi-clientd_${VERSION}_${goos}_${goarch}.tar.gz"
SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-$(git -C "$REPO" show -s --format=%ct HEAD)}" \
  python3 "$CLIENTD/scripts/archive-release.py" "$stage" "$bundle" >/dev/null
if [ "$target" = linux-amd64 ]; then
  python3 - "$output_dir/zkapi-clientd_${VERSION}_${goos}_${goarch}.build-environment.json" \
    "$VERSION" "$GO_VERSION" "$commit" "$first_daemon" "$second_daemon" \
    "$first_confine" "$second_confine" <<'PY'
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
metadata = {
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
    json.dump(metadata, output, indent=2, sort_keys=True)
    output.write("\n")
PY
fi
if [ "$target" = linux-amd64 ]; then
  echo "$target: zkapi-clientd $first_daemon, zkapi-confine $first_confine (reproducible, $GO_VERSION)"
else
  echo "$target: zkapi-clientd $first_daemon (reproducible, $GO_VERSION)"
fi
