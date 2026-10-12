# Olympus fork of zkapi-clientd

Maintained fork of `zkapi-clientd` (Go) from https://github.com/ethereum/zkapi,
carrying opt-in supervisor and managed-companion custody patches plus a
standalone Linux x64 confinement launcher and reproducible build. Nothing here changes wallet, key, or protocol code. The
Rust companion is not built or modified here.

## Why a fork

Olympus runs the daemon as a supervised child and wants to confine its network
use to one Tor SOCKS5 listener (macOS Seatbelt profile). Upstream reads its
relay endpoint only from a config file that also holds the wallet bridge token,
binds the companion's CONNECT proxy on a random loopback port (which a profile
written before start cannot name), and does not report its effective endpoints.
The patch fixes those three things without touching `config.json`. If upstream
ships equivalent flags, this fork retires.

## What the patch does

All options are opt-in and apply to one `serve` run only (never saved). With no
flags, behaviour is unchanged from upstream.

| Option | Effect |
|---|---|
| `--relay-url URL` | Overrides `relay_url` in memory (Wisp, or loopback `socks5://127.0.0.1:PORT`). |
| `--companion-proxy-listen IP:PORT` | Binds the companion CONNECT proxy to exactly this address (`127.0.0.1` or `[::1]`, nonzero port). A bind failure exits; no fallback port. |
| `--wallet-api-listen IP:PORT` | Fixed loopback address for the managed companion API; rewrites `zkapi.client_url` in memory. |
| `--require-managed-companion` | Refuses to start if `external_companion` is configured (the two listen flags imply it). |
| `--require-companion-custody` | macOS only: before every new wallet HTTP connection, dials without sending bytes, proves the supervised child owns the reverse endpoint of that exact established socket, and fails closed otherwise. |

`/admin/status` (API bearer required) gains a `transport` object: `kind`,
`relay_endpoint`, `companion`, `wallet_api`, `connect_proxy`, and, for a
custody-gated run, `wallet_custody: "connection_owner_verified"`; no credentials
or query secrets. The custody gate binds the child lifetime with a Darwin
kqueue process event and checks the connected socket's exact reversed four-tuple
through `/usr/sbin/lsof`. The same socket is then handed to `net/http`, so a
listener replacement cannot receive the saved bridge token. Before child start,
after child exit, on an unowned listener, or when ownership inspection fails,
the transport sends no application bytes. Unsupported platforms refuse this
opt-in flag.

## Pins

- Upstream: `ethereum/zkapi` commit `045b444ea1b52538d1b40273c7cb6ed09468a052` (main, 2026-10-01).
- Release tag: `clientd-v0.1.6` (an ancestor of the pin; plus three package-pin commits).
- Fork version string: `0.1.6-olympus2`.
- Go toolchain: `go1.27.1` (`GOTOOLCHAIN` is set by `build.sh`).
- Branch: `olympus/supervisor-flags`.

## Build

```
zkapi-clientd/olympus/build.sh
```

The default builds darwin/arm64 with `CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w -X main.version=0.1.6-olympus2"`,
writes `dist/zkapi-clientd`, `dist/SHA256SUMS` and `dist/third-party/` (licence
texts for the linked Go modules), then rebuilds from a clean `git archive` copy
in a temp dir and fails if the hashes differ. It builds committed `HEAD`
for the second pass, so commit before trusting the check.

The `olympus1` build was reproducible only within one toolchain distribution;
Homebrew and GitHub's official Go distribution produced different hashes from
the same source. Publish the `olympus2` hash from one named build environment,
and do not claim cross-machine reproducibility until that toolchain difference
is resolved.

Unit tests run on Linux in CI and locally: `cd zkapi-clientd && gofmt -l . && go vet ./... && go test -race ./...`.

### Verifying a build yourself

1. `git clone https://github.com/jamiezigelbaum/zkapi && git checkout <tag or commit>`.
2. `git diff 045b444ea1b52538d1b40273c7cb6ed09468a052 HEAD -- zkapi-clientd` shows every change from upstream.
3. Install the pinned Go, run `zkapi-clientd/olympus/build.sh`, compare `dist/SHA256SUMS`
   with the published hash. A reproducible build shows the binary matches this
   source; it does not show who built the published one (provenance).

## Linux x64 confinement launcher (W4 step 1)

`cmd/zkapi-confine` ports the reviewed Olympus prototype at `afdb5406`.
It is standard-library-only and runs only on Linux amd64. Other targets compile
only a refusal stub (exit 78); the Linux code also checks the runtime platform.
The three-stage boundary preserves user/network/PID namespaces, capability
removal, no-new-privileges, Landlock ABI >= 4, seccomp, inherited-descriptor
sanitization, private status fd 3, authenticated API bridge and fixed Tor bridge.
`--version`, `run`, `check` and `probe` are supported; stage entry points and
`noop` serve the launcher itself. Runtime failure injection, ABI emulation and
pair-type overrides have been removed. Production ignores all former prototype
environment switches. Test-only ablations require the `qualification` build tag.

```sh
bash zkapi-clientd/olympus/build.sh linux-amd64
zkapi-clientd/olympus/dist/bin/zkapi-confine --version
zkapi-clientd/olympus/dist/bin/zkapi-confine check
```

The Linux build includes the daemon and `bin/zkapi-confine`, compares both
against a second build from committed HEAD, and records both build hashes and
the build environment under `olympus/dist/`. The Linux x64 release bundle adds
`bin/zkapi-confine`; Mac bundles retain their existing layout and metadata.
The version remains `0.1.6-olympus2` until the owner's separate release step.
Do not publish these changes under an existing immutable release tag.

Unit tests cover arguments, the prototype seccomp golden and policy decisions,
control descriptor passing, harden-before-go ordering, HMAC authentication,
bridge byte pumping/pending-slot limits, check classification and descriptor
fallback failure. The Linux CI job adds reproducibility and environment artifacts.

The [host qualification instructions](../cmd/zkapi-confine/qualification/README.md)
run normal rows against the release binary and negative controls against
separate tagged fixtures. A real Omarchy-class host run remains required;
a build-container refusal is not a qualification pass. This commit does not
activate confinement in Olympus or change release publishing triggers.

Preserved prototype limits: network confinement leaves file access unrestricted;
bridge slots limit pending handoffs rather than all established streams. The
only extra protocol cleanup closes an unexpected descriptor received with the
initial go message. No wire-format or intended confinement-policy change.

## Rebasing on upstream

```
git remote add upstream https://github.com/ethereum/zkapi
git fetch upstream --tags
git checkout -b olympus/rebase-<tag> <new upstream tag or commit>
git apply --3way zkapi-clientd/olympus/olympus-supervisor-flags.patch   # patch lives on the fork branch; use `git show olympus/supervisor-flags:...` if needed
cd zkapi-clientd && gofmt -l . && go vet ./... && go test -race ./...
```

Then bump the pins above, the version string in `build.sh`, and re-run it.
Regenerate the patch with `git diff <upstream pin> HEAD -- zkapi-clientd ':!zkapi-clientd/olympus'`.

Applicability check (2026-10-07): upstream `main` is still `045b444` (identical to
the pin, 0 commits ahead); the newest tag is `clientd-v0.1.6`. The patch applies
cleanly to both (`git apply --check`). It also applies to v0.1.5; it does not
apply to v0.1.2 to v0.1.4, which predate SOCKS5 support.

## Release

`zkapi-clientd/olympus/bundle.sh TARGET UPSTREAM_ARCHIVE OUT` makes one release
bundle: upstream's `clientd-v0.1.6` archive (hash checked against
`packaging/releases/clientd-0.1.6.json`) with `zkapi-clientd` replaced by this
fork's build (built twice, refused if they differ), `VERSION` set to the fork
version, `NOTICE-modified.md`, and `share/zkapi-clientd/olympus-build.json`
recording both hashes. `zkapi-walletd` and the proof-setup files stay
upstream's bytes. Pushing a `clientd-v<version>` tag that matches `build.sh`
runs `.github/workflows/olympus-release.yml`: four bundles, a smoke run on each
platform's own runner, `SHA256SUMS`, build-provenance attestations
(`gh attestation verify <file> -R jamiezigelbaum/zkapi`), and a published
release. Olympus pins those hashes in `src/core/managed-tools.ts`.

## Not done

- **No signing or notarization.** The binary is unsigned and not notarized;
  this must be added (Developer ID, hardened runtime) before shipping inside a
  signed app. CI artifacts are unsigned.
- The upstream companion (`zkapi-walletd`) and upstream's
  `share/zkapi-clientd/` bundle are not built or vendored here; take them from
  upstream's release tarball, verified against
  `zkapi-clientd/packaging/releases/clientd-<ver>.json`.
- The patch is not proposed upstream.

## Licences and notices

MIT (`LICENSE`, copyright Open Anonymity Team), `NOTICE-modified.md` (modified-source
statement and the go-ethereum LGPL-3.0 note), and per-module licence texts generated
into `dist/third-party/` by `build.sh`. Upstream's own third-party tree is generated
at its release time and is not in the source tree.
