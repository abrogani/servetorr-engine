# ServeTorr engine

The torrent engine used by the [ServeTorr](https://github.com/abrogani/automatic-sniffle/tree/main/providers/ServeTorr)
Flixclusive provider: our own gomobile build of [YouROK/TorrServer](https://github.com/YouROK/TorrServer), released as
`torrserver.aar`. The provider downloads it on demand, verifies it against pinned hashes and loads it in-process.

Licensed under GPL-3.0 (see `LICENSE`), like upstream TorrServer, which this is a derivative of.

## Layout

| Path | Contents |
|---|---|
| `bindings/` | The `torrServer` gomobile package (`torrserver.go`), its `go.mod`/`go.sum`, and the smoke tests. |
| `patches/` | Patches applied to the upstream checkout (`upstream-*.patch`) and to the gobind-generated Java. |
| `build.sh` | Pins (upstream commit, Go, gomobile, Android API, build tools) and the build. `build.sh test` runs the smoke tests. |
| `scripts/` | `check-patches.sh` (per-patch apply report) and `update-engine-source.sh` (rewrites the provider's pins). |
| `.github/` | `release.yml`, `upstream-bump.yml` and the shared toolchain action. |

## Upstream

`build.sh` pins `UPSTREAM_COMMIT`, a commit on upstream's default branch. The build clones upstream at that commit,
applies `patches/upstream-*.patch`, and drops `bindings/` into `server/mobile/servetorr` as its own module (with
`server => ../..`).

## What we change

- `bindings/torrserver.go`: the `torrServer` gomobile package. `startTorrentServer(path, port)` returns the real port
  (port 0 picks a free one) or `-1`, and never exits the process. Also `stopTorrentServer`, `addTrackers`,
  `waitTorrentServer`. Only the HTTP API is linked: no web UI, Telegram bot, MCP, DLNA or Bonjour.
- Binds `127.0.0.1` only (upstream's bind option). Requests carrying `Origin` or `Sec-Fetch-Site` get `403`, so web
  pages can't drive it.
- `/echo` answers `ServeTorr torrserver <VERSION>`.
- `upstream-0001`: DLNA/Bonjour stubs under the `servetorr` build tag.
- `upstream-0002`: reopen the settings DB after stop, so stop → start works in one process.
- `upstream-0003`: stop cancels any in-flight `/settings` reconnect loop (epoch counter), and the trackers-list refresh
  pauses while stopped.
- `gobind-java-lazy-load.patch`: adds a lazy `go.Seq.load()`. Stock gomobile loads the library in a static initializer;
  the provider must load it through a `DexClassLoader` whose library path is the engine dir.

## Building

```sh
ANDROID_HOME=… ANDROID_NDK_HOME=… ./build.sh [out-dir]
```

- Requires Go 1.27.4, a JDK (Temurin 25.0.2 in CI), NDK 28.2.13676358, build-tools 36.1.0 and platform android-36.
  Runs on macOS or Linux.
- Output is reproducible: the build runs in a fixed `/tmp/servetorr-engine` with `-trimpath` and no build ID.
- Writes `torrserver.aar` and `sha256.txt` to `out-dir` (default `build/`). The AAR contains `jni/<abi>/libgojni.so`
  for arm64-v8a, armeabi-v7a, x86 and x86_64 (16 KB aligned on 64-bit) and a top-level `classes.dex` (the d8'd
  bindings). `sha256.txt` lists the hashes of those five files.

## Smoke tests

```sh
./build.sh test [go test flags, e.g. -v -run Settings]
```

Prepares the patched upstream tree in `/tmp/servetorr-engine-test` and runs `go test` on the bindings on the host. No
Android toolchain and no internet needed (the torrent used is built in the test). They pin the upstream behaviour the
provider relies on, so an upstream bump can't silently break it:

| Test | Checks |
|---|---|
| `TestStartReturnsRealLoopbackPort` | Port 0 returns a real port, reachable on `127.0.0.1` and not on other interfaces; a second start returns the same port. |
| `TestEchoIsExactSignature` | `/echo` is exactly `ServeTorr torrserver <tag>`. |
| `TestBrowserRequestsAreRejected` | `Origin` / `Sec-Fetch-Site` get `403` on every route. |
| `TestSettingsPayloadShape` | `/settings get` has every key the provider reads or writes, with the right JSON type. |
| `TestSettingsSetMergedRoundTripsAndDefResets` | get → merge → `set` of the full object round-trips and leaves other keys alone; `def` restores defaults. |
| `TestTorrentsLifecycle` | `add` returns immediately without metadata; `get`/`list`/`drop`/`rem`/`wipe`; `file_stats` ids are 1-based and consecutive in natural path order; `/stream?link=<info-hash magnet>&index=<id>` serves that file and 404s for unknown ids. |
| `TestViewedListAndRemove` | `/viewed list` is an array; `rem` with `file_index: -1` is accepted. |
| `TestStopThenStartInOneProcess` | stop → start three times in one process; `waitTorrentServer` returns on stop; the port is closed after stop. |
| `TestStopCancelsReconnectLoop` | Stop during a pending `/settings set` reconnect cancels it and frees the peer port. |
| `TestStopStartDuringPendingSet` | Stop → start during a pending `set` leaves the new engine holding the peer port, and the final stop frees it. |

## Versioning

The engine version is a plain integer. These move together:

| Where | Value |
|---|---|
| `TAG` in `build.sh` | `1` |
| `const tag` in `bindings/torrserver.go` | `1` |
| Git tag / GitHub release | `v1` |
| `/echo` | `ServeTorr torrserver 1` |
| `EngineSource.VERSION` in the provider | `1` |

Bump it for any change that changes the binary: upstream commit, patches, bindings or toolchain pins.

## Release

1. Bump `TAG` and `const tag` (the upstream bump PR does this), merge to `main`.
2. Tag the commit `v<VERSION>` and push the tag (or run **Release** manually with the version; it refuses to run if
   the tag exists and points elsewhere).
3. `release.yml` checks the version constants, runs the smoke tests, builds, checks `sha256.txt` against the AAR, and
   publishes release `v<VERSION>` with `torrserver.aar` and `sha256.txt`. The job summary has the test results and the
   `EngineSource` block.
4. If `PROVIDER_REPO_TOKEN` is set, it opens (or updates) a PR on `abrogani/automatic-sniffle` from branch
   `servetorr-engine-v<VERSION>` that changes only `VERSION`, `DEX_SHA256` and `LIBRARY_SHA256` in `EngineSource.kt`
   (`scripts/update-engine-source.sh`: exact-line replacement, fails if the file's shape changed). Without the
   secret the step is skipped with a notice; paste the block from the summary by hand.

**The CI (Linux) build is the source of truth** for the pinned hashes.

## Upstream updates

`upstream-bump.yml` runs daily (and manually, optionally with a specific upstream `commit` and `force`).

- It tracks the **head of upstream's default branch**, not releases: our pin and patches are against that branch, and
  upstream's tags mix lineages (`MatriX.*`, `Silo.*`) that our patches don't apply to.
- If the head differs from `UPSTREAM_COMMIT`, it creates `bump/upstream-<12-char sha>`, updates the pin, bumps
  `TAG`/`const tag` by one, reports each patch (with the `git apply --reject` output for failures), runs the smoke
  tests and the build, commits, and opens a PR. The PR body has the upstream compare link, patch/test/build results,
  the new hashes and the `EngineSource` block.
- A failing bump still opens the PR, as a draft titled `[FAILING] …`, and the job fails. Fix the patches on that
  branch.
- No duplicates: if an open PR for that branch exists, the run is skipped (`force` rebuilds and force-pushes the branch,
  overwriting any commits on it, and updates the PR). Older open `bump/upstream-*` PRs are closed as superseded (their
  branches are kept).
- PRs opened with `GITHUB_TOKEN` don't trigger other workflows; that's why all checks run in this job and land in the
  PR body. Nothing else needs to run on the PR.

## Secrets and settings

| Name | Used by | Notes |
|---|---|---|
| `GITHUB_TOKEN` | both workflows | Built in. Enable *Settings → Actions → General → Allow GitHub Actions to create and approve pull requests* for the bump PRs. |
| `PROVIDER_REPO_TOKEN` | `release.yml` | Optional. Fine-grained PAT with *Contents* and *Pull requests* read/write on `abrogani/automatic-sniffle`. |

## How the provider consumes it

`EngineSource.kt` in the provider pins:

- `VERSION`, and `AAR_URL = "https://github.com/abrogani/servetorr-engine/releases/download/v$VERSION/torrserver.aar"`.
- `DEX_SHA256` and `LIBRARY_SHA256` (per ABI), from this repo's `sha256.txt`.

The provider range-requests the AAR's ZIP tail, fetches only `jni/<abi>/libgojni.so` and `classes.dex`, checks each
against its pin, and loads the dex through a `DexClassLoader` with the engine dir as its library path. It requires
`/echo` to match `ServeTorr torrserver <VERSION>` exactly.
