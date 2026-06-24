# pingtop

<p>
  <a href="https://github.com/tfindleton/pingtop/actions/workflows/ci.yml"><img alt="CI" src="https://img.shields.io/github/actions/workflow/status/tfindleton/pingtop/ci.yml?branch=main&label=ci&logo=githubactions"></a>
  <a href="https://github.com/tfindleton/pingtop/blob/main/go.mod"><img alt="Go Version" src="https://img.shields.io/github/go-mod/go-version/tfindleton/pingtop?logo=go"></a>
  <a href="https://goreportcard.com/report/github.com/tfindleton/pingtop"><img alt="Go Report Card" src="https://goreportcard.com/badge/github.com/tfindleton/pingtop"></a>
</p>

<p>
  <a href="https://github.com/tfindleton/pingtop/releases"><img alt="Release" src="https://img.shields.io/github/v/release/tfindleton/pingtop?display_name=tag&sort=semver&logo=github"></a>
  <a href="https://github.com/tfindleton/pingtop/releases"><img alt="Downloads" src="https://img.shields.io/github/downloads/tfindleton/pingtop/latest/total?logo=github"></a>
  <a href="https://github.com/tfindleton/pingtop/releases"><img alt="Platforms" src="https://img.shields.io/badge/platform-linux%20%7C%20macOS%20%7C%20windows-1f2937"></a>
  <a href="https://github.com/tfindleton/pingtop/releases"><img alt="Arch" src="https://img.shields.io/badge/arch-amd64%20%7C%20arm64%20%7C%20armv7-2563eb"></a>
</p>

`pingtop` is a terminal network monitor written in Go.

![pingtop demo](demo.gif)

## What It Does

- live terminal UI with redraws, color, and keyboard controls
- concurrent ping and DNS checks with simple failure classification
- cancellable check cycles with live checking, stale, and age indicators
- interactive mode and headless mode
- quiet logging modes, including only state changes or no logging
- single-binary releases for Linux, macOS, and Windows
- ad hoc target runs from the command line without writing CSV logs

## Downloads

Prebuilt binaries are published on [GitHub Releases](https://github.com/tfindleton/pingtop/releases).

- platforms: Linux, macOS, Windows
- architectures: `amd64`, `arm64`, Linux `armv7`
- packaging: single binary per archive, no Go runtime required
- size: release asset sizes vary by platform and version; see the latest release assets for exact download sizes

## Quick Start

Run from source:

```bash
go run .           # interactive UI when a supported TTY is available
go run . -n        # headless mode
go run . -o        # single pass
go run . -v        # version
go run . -h        # help
go run . -u        # one-shot update check
go run . --updates
go run . 1.1.1.1
go run . example.com 1.1.1.1
```

Build locally:

```bash
go build -o pingtop
./pingtop
./pingtop -h
./pingtop -v
./pingtop -u
./pingtop --updates
```

Build `pingtop.exe` on Windows:

```powershell
go build -o pingtop.exe .
.\pingtop.exe
.\pingtop.exe -h
.\pingtop.exe -v
.\pingtop.exe -u
.\pingtop.exe --updates
```

Passing one or more positional targets overrides the configured target list for that run only. Those ad hoc runs keep the normal UI or headless behavior, but CSV logging is disabled for that session.

To debug update detection without publishing a new release, run:

```bash
pingtop -u
pingtop --updates
pingtop -u --current-version 0.1.3
pingtop --check-updates --current-version 0.1.3 --update-repo https://github.com/tfindleton/pingtop
```

## Controls

The interactive UI starts with help and events visible and target details hidden by default. It remembers those visibility choices in `pingtop.json`.

Runtime files are written next to the `pingtop` executable: `pingtop.json` for targets and settings, `pingtop_log.csv` for CSV logs, and `pingtop_snapshot_*.txt` for snapshots. When running with `go run`, pingtop uses the launch/current directory instead of Go's temporary build directory.

**Controls**
- `q` or `Esc`: quit
- `p`: pause or resume
- `h`: show or hide help
- `i`: show or hide selected-target details
- `e`: show or hide events
- `f`: force a fresh check cycle
- `s`: save a snapshot
- `r`: reset session counters
- `u`: open the release page
- `PgUp` / `PgDn`: page through event history

**Tuning**
- `l`: cycle logging mode (`around_failure`, `changes_only`, `failures_only`, `all`, `off`)
- `+` / `-`: increase or decrease the check interval (`0.50s` to `300.00s`)
- `<` / `>`: decrease or increase the UI refresh interval (`0.10s` to `5.00s`)
- `w`: set the around-failure window
- `t`: set the stats window

**Targets**
- `a`: add a target
- `d`: confirm deleting the selected target
- `D`: delete by target index or exact target
- `Up` / `Down` or `j` / `k`: move the target selector
- `Space` or `Enter`: enable or disable the selected target

## Release Flow

Before tagging a release, update [`internal/pingtop/version.go`](internal/pingtop/version.go) so `Version` matches the tag value without the optional leading `v`.

Then push either tag format:

```bash
git tag 0.1.3
git push origin 0.1.3
```

or:

```bash
git tag v0.1.3
git push origin v0.1.3
```

The release workflow verifies that the tag and source version match, runs tests, builds release archives, and attaches them to the GitHub release automatically.

## Tests

```bash
go test ./...
```
