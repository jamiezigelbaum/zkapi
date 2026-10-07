# Olympus fork of zkapi-clientd

Maintained fork of `zkapi-clientd` (Go) from https://github.com/ethereum/zkapi,
carrying one small, opt-in supervisor patch and a reproducible build. Nothing
here changes wallet, key, or protocol code. The Rust companion is not built or
modified here.

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

`/admin/status` (API bearer required) gains a `transport` object: `kind`,
`relay_endpoint`, `companion`, `wallet_api`, `connect_proxy`; no credentials or
query secrets. Full per-file description: the commit message and
`olympus-supervisor-flags.patch` (same change as the commit).

## Pins

- Upstream: `ethereum/zkapi` commit `045b444ea1b52538d1b40273c7cb6ed09468a052` (main, 2026-10-01).
- Release tag: `clientd-v0.1.6` (an ancestor of the pin; plus three package-pin commits).
- Fork version string: `0.1.6-olympus1`.
- Go toolchain: `go1.27.1` (`GOTOOLCHAIN` is set by `build.sh`).
- Branch: `olympus/supervisor-flags`.

## Build

```
zkapi-clientd/olympus/build.sh
```

Builds darwin/arm64 with `CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w -X main.version=0.1.6-olympus1"`,
writes `dist/zkapi-clientd`, `dist/SHA256SUMS` and `dist/third-party/` (licence
texts for the linked Go modules), then rebuilds from a clean `git archive` copy
in a temp dir and fails if the hashes differ. It builds committed `HEAD`
for the second pass, so commit before trusting the check.

Reference result (go1.27.1, 2026-10-07, macOS arm64): SHA-256
`bffc86b70cbf409383970160f15f042ae57b8175d8102db7bcad4ea2ddbc37de`, 9,694,658
bytes; identical to the earlier prototype build (`bffc86b7...`).

Unit tests run on Linux in CI and locally: `cd zkapi-clientd && gofmt -l . && go vet ./... && go test -race ./...`.

### Verifying a build yourself

1. `git clone https://github.com/jamiezigelbaum/zkapi && git checkout <tag or commit>`.
2. `git diff 045b444ea1b52538d1b40273c7cb6ed09468a052 HEAD -- zkapi-clientd` shows every change from upstream.
3. Install the pinned Go, run `zkapi-clientd/olympus/build.sh`, compare `dist/SHA256SUMS`
   with the published hash. A reproducible build shows the binary matches this
   source; it does not show who built the published one (provenance).

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

## TODO (F2 prerequisite, not done here)

**TODO(F2): authenticated ownership of the managed wallet listener.** The
patched daemon still sends its wallet bridge token to whatever process holds the
wallet-API port during readiness polling, before ownership is verified (same
exposure as upstream's configured `client_url`; a sandbox does not prevent it).
Fix needed before any "route verified" claim: a child startup handshake or a
daemon-side equivalent that proves the listener is the companion this process
started, before the first token-bearing request. A "port free" check is not enough.

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
