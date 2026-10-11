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
[ "$(go env GOVERSION)" = "$GO_VERSION" ] || { echo "bundle.sh: expected $GO_VERSION, got $(go env GOVERSION)" >&2; exit 1; }

work="$(mktemp -d)"; trap 'rm -rf "$work"' EXIT
build() { ( cd "$1" && go build -trimpath -buildvcs=false -ldflags="-s -w -X main.version=$VERSION" -o "$2" ./cmd/zkapi-clientd ); }

build "$CLIENTD" "$work/first"
subdir="$(python3 -c 'import os,sys; print(os.path.relpath(sys.argv[1], sys.argv[2]))' "$CLIENTD" "$REPO")"
mkdir "$work/src"
git -C "$REPO" archive --format=tar HEAD "$subdir" | tar -x -C "$work/src"
build "$work/src/$subdir" "$work/second"
[ "$(sha "$work/first")" = "$(sha "$work/second")" ] || { echo "bundle.sh: NOT REPRODUCIBLE for $target" >&2; exit 1; }

stage="$work/stage"; mkdir "$stage"
tar -xzf "$upstream" -C "$stage"
[ "$(cat "$stage/VERSION")" = "$UPSTREAM_VERSION" ] || { echo "bundle.sh: upstream VERSION is not $UPSTREAM_VERSION" >&2; exit 1; }
for file in zkapi-clientd zkapi-walletd share/zkapi-clientd/build-info.json \
  share/zkapi-clientd/proof-setup/manifest.json share/zkapi-clientd/proof-setup/request.pk share/zkapi-clientd/proof-setup/request.vk \
  share/zkapi-clientd/proof-setup/withdrawal.pk share/zkapi-clientd/proof-setup/withdrawal.vk; do
  test -s "$stage/$file" || { echo "bundle.sh: upstream archive lacks $file" >&2; exit 1; }
done

install -m 755 "$work/first" "$stage/zkapi-clientd"
printf '%s\n' "$VERSION" > "$stage/VERSION"
cp "$HERE/NOTICE-modified.md" "$stage/NOTICE-modified.md"
commit="$(git -C "$REPO" rev-parse HEAD)"
python3 - "$stage/share/zkapi-clientd/olympus-build.json" <<EOF
import json, sys
json.dump({
  "version": "$VERSION",
  "fork": "https://github.com/jamiezigelbaum/zkapi",
  "fork_commit": "$commit",
  "go_toolchain": "$GO_VERSION",
  "zkapi_clientd_sha256": "$(sha "$work/first")",
  "upstream_tag": "$UPSTREAM_TAG",
  "upstream_archive": "$name",
  "upstream_archive_sha256": "$want",
  "unchanged_from_upstream": "everything except zkapi-clientd, VERSION, NOTICE-modified.md and this file"
}, open(sys.argv[1], "w"), indent=2, sort_keys=True)
open(sys.argv[1], "a").write("\n")
EOF

mkdir -p "$output_dir"
SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-$(git -C "$REPO" show -s --format=%ct HEAD)}" \
  python3 "$CLIENTD/scripts/archive-release.py" "$stage" "$output_dir/zkapi-clientd_${VERSION}_${goos}_${goarch}.tar.gz" >/dev/null
echo "$target: zkapi-clientd $(sha "$work/first") (reproducible, $GO_VERSION)"
