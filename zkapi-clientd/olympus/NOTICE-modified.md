# Modified source notice

This directory tree is a modified version of `zkapi-clientd` from
https://github.com/ethereum/zkapi, copyright (c) 2026 Open Anonymity Team,
licensed under the MIT License (see `LICENSE` in this directory and in the
parent directory).

The modifications are the commit on branch `olympus/supervisor-flags`
("clientd: opt-in supervisor flags for serve and transport status"), applied
on upstream commit `045b444ea1b52538d1b40273c7cb6ed09468a052`
(`clientd-v0.1.6` plus three package-pin commits). The same change is kept as a
patch file, `olympus-supervisor-flags.patch`, in this directory, and
`git diff 045b444e..olympus/supervisor-flags -- zkapi-clientd` shows it exactly.
Modified files: `cmd/zkapi-clientd/main.go`, `internal/config/config.go`,
`internal/relay/connect.go`, `internal/relay/relay.go`,
`internal/server/server.go`; new test files only otherwise. Files under
`olympus/` are additions by the Olympus project.

Not affiliated with, or endorsed by, the upstream authors.

## go-ethereum (LGPL-3.0)

The binary links `github.com/ethereum/go-ethereum` (pinned in `go.mod`), the
library parts of which are licensed under the GNU Lesser General Public License
v3.0 (some packages and `cmd/` under GPL-3.0; only the library packages
upstream imports are linked). Its licence text is in
`dist/third-party/github.com_ethereum_go-ethereum@<version>/` after running
`build.sh`. Source for the exact version linked is at
https://github.com/ethereum/go-ethereum at the tag in `go.mod`; the complete
corresponding source of this daemon is this repository. The binary is
statically linked; a recipient can relink with a modified go-ethereum by
changing the `go.mod` version and running `build.sh`.

## Other dependencies

Licence texts for every Go module linked into the binary (and for the Go
standard library, BSD-3-Clause) are produced by `build.sh` into
`dist/third-party/`, listed in `MODULES.txt`. Upstream's
`share/zkapi-clientd/third-party/` tree is generated at upstream release time
(it also covers the Rust companion) and is not in the source tree; a
distribution that includes the upstream companion must carry upstream's own
tree from that release tarball.
