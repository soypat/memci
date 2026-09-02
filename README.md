# memci

Reports what a pull request did to your **benchmark allocations** and your
**binary size**, and nothing else. Rows that did not move are dropped, so an
empty report means nothing got worse.

```
#### Binary size

Total +258.52 KiB: `httpsrv` +258.52 KiB.

**httpsrv — +258.52 KiB**

| package | base | head | Δ |  |
| --- | ---: | ---: | ---: | ---: |
| encoding/json | — | 57.71 KiB | +57.71 KiB | new |
| slices | 68.01 KiB | 101.46 KiB | +33.45 KiB | +49.2% |

#### Benchmark allocations

**allocs/op**

| Package | Benchmark | base | head | Δ |  |
| --- | --- | ---: | ---: | ---: | ---: |
| . | BenchmarkGreet | 1 | 2 | +1 | +100.0% |
```

With `-tinygo`, every binary is built by both toolchains and the totals sit next
to each other.


It deliberately does not report `ns/op`. Timings measure the runner; `B/op`,
`allocs/op` and binary bytes measure the code.

## How it measures

Both revisions are built and benchmarked **in the same job, on the same runner,
with the same toolchain**, by adding a `git worktree` for the base branch. There
is no stored baseline to expire, to be missing on a first run, or to have been
produced by a different Go version.

Binary size comes from [`bindiff`](https://github.com/soypat/tinyboot), which
attributes every byte of an ELF to a segment, section, symbol, package, source
file or line. Builds use `-trimpath -buildvcs=false`, which makes them
reproducible, so a byte of difference is a byte the change caused.

### TinyGo

`-tinygo` adds a second toolchain: the same targets are built again with
`tinygo build`, diffed separately, and reported next to the host numbers. A
change that is free on a host binary can be expensive on a microcontroller —
one interface method that forces a whole reflect path into the firmware — and
one number cannot show both. `-tinygo-flags` carries the target, and
`-tinygo-targets` narrows the set when only some commands cross-compile:

```sh
go run github.com/soypat/memci@latest -base /tmp/base \
  -targets ./cmd/... -tinygo tinygo -tinygo-targets ./cmd/firmware \
  -tinygo-flags "-target=pico -opt=z"
```

The two toolchains are not measured the same way, and the report says so under
the totals. Go binaries are compared as **file bytes**: that is what ships, and
their `.bss` is a virtual reservation nobody pays for — the Go runtime alone
reserves tens of megabytes of untouched `.noptrbss`, which would swamp the total
and dilute every percentage in the table. TinyGo binaries are compared as the
**loadable image plus `.bss`** (bindiff's `-mem`), because on a device that is
the memory that has to exist, and because `tinygo build` has no `-trimpath`: the
checkout path reaches the DWARF, so two builds of identical source differ by the
length of the directory they were built in. None of those bytes are loadable, so
the image is reproducible where the file is not.

Benchmarks stay a Go-only measurement; `-bench` is unaffected by `-tinygo`.

`B/op` is an average over iterations, so the default `-benchtime=1000x` pins
both sides to the same iteration count; with a duration the two runs average
over different counts and the column wobbles on its own. What wobble remains is
absorbed by `-tol-bytes` and `-tol-pct`. Allocation counts and binary sizes are
exact and have no tolerance applied.

## Use it

The measuring job runs PR code, so it gets no write permissions. A second,
privileged workflow posts the report. This is the standard split for
`pull_request_target`-free commenting and is why there are two files.

`.github/workflows/memci.yml`:

```yaml
name: memci
on:
  pull_request:
    branches: [main]
permissions: read-all
jobs:
  memci:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v6
      - uses: actions/setup-go@v6
        with: { go-version: "1.26" }
      - uses: soypat/memci@v1
        with:
          args: -targets ./cmd/...
```

`.github/workflows/memci-comment.yml`:

```yaml
name: memci comment
on:
  workflow_run:
    workflows: [memci]
    types: [completed]
permissions: read-all
jobs:
  comment:
    if: github.event.workflow_run.event == 'pull_request'
    runs-on: ubuntu-latest
    permissions:
      contents: read
      pull-requests: write
    steps:
      - uses: soypat/memci/comment@v1
        with:
          run-id: ${{ github.event.workflow_run.id }}
```

Drop the second file if the job summary is enough; the first one writes the
report there either way.

### Action inputs

| input | default | |
|---|---|---|
| `args` | `-targets ./...` | Flags for memci, below. |
| `bindiff` | `github.com/soypat/tinyboot/cmd/bindiff@latest` | Installed if it contains `@`, otherwise run as a command. |
| `tinygo` | | TinyGo command to size the targets with as well. Empty skips TinyGo. |
| `tinygo-targets` | | Package patterns to build with TinyGo. Defaults to the `-targets` in `args`. |
| `tinygo-flags` | | Extra flags for `tinygo build`, e.g. `-target=pico`. |

`tinygo` and its two companions are inputs of their own rather than part of
`args` because `args` is word-split by the shell, so a flag list containing a
space cannot survive it.

Leaving `tinygo` empty is the whole of turning TinyGo off: nothing is installed,
nothing is built with it, and the setup step below is not needed. When it is
set, the action still does not install TinyGo — the workflow does.

A firmware repo's `.github/workflows/memci.yml`, sizing every command with the
host toolchain and the one that ships to the board with both:

```yaml
name: memci
on:
  pull_request:
    branches: [main]
permissions: read-all
jobs:
  memci:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v6
      - uses: actions/setup-go@v6
        with: { go-version: "1.26" }
      # Only needed because tinygo is set below. Keep the two versions in step:
      # TinyGo 0.42 builds with Go 1.25 through 1.27 and refuses to run outside
      # that window, in either direction.
      - uses: acifani/setup-tinygo@v2
        with:
          tinygo-version: "0.42.0"
      - uses: soypat/memci@v1
        with:
          args: -targets ./cmd/... -kind package
          tinygo: tinygo
          tinygo-targets: ./cmd/firmware
          tinygo-flags: -target=pico
```

The comment workflow above is unchanged: the report is one artifact either way.

## Run it locally

```sh
git worktree add /tmp/base origin/main
go run github.com/soypat/memci@latest -base /tmp/base -targets ./cmd/...
```

| flag | default | |
|---|---|---|
| `-base` | | Checkout to compare against. Required. |
| `-head` | `.` | Checkout under test. |
| `-targets` | | Package patterns to build and size-profile. Empty skips binary sizes. |
| `-bench` | `./...` | Package pattern to benchmark. Empty skips benchmarks. |
| `-tinygo` | | TinyGo command to build the targets with as well. Empty skips TinyGo. |
| `-tinygo-targets` | | Package patterns to build with TinyGo. Defaults to `-targets`. |
| `-tinygo-flags` | | Extra flags for `tinygo build`, e.g. `-target=pico -opt=z`. |
| `-kind` | `package` | Size granularity: `segment`, `section`, `symbol`, `package`, `file`, `line`. |
| `-count` | `5` | Benchmark runs; each metric is the median. |
| `-benchtime` | `1000x` | Fixed iteration counts keep `B/op` comparable. |
| `-tol-bytes` | `8` | Ignore `B/op` changes below this many bytes. |
| `-tol-pct` | `1` | Ignore `B/op` changes below this percentage. |
| `-top` | `25` | Rows per table, largest changes first. |
| `-fail-on-growth` | `-1` | Exit non-zero past this many bytes of growth, per toolchain. |
| `-bindiff` | `bindiff` | Command to run; may include arguments, e.g. `go run ./cmd/bindiff`. |

`-kind` is worth tuning to the binary. For firmware, `segment` is the flash
footprint and `package` explains it. For host binaries much of the file is
DWARF, which lands in bindiff's `[unattributed]` rows.

Inside tinyboot itself, point `-bindiff` at the local copy rather than
installing one:

```sh
go run github.com/soypat/memci@latest -base /tmp/base -targets ./cmd/... \
  -bindiff "go run ./cmd/bindiff"
```
