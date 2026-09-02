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
| `-kind` | `package` | Size granularity: `segment`, `section`, `symbol`, `package`, `file`, `line`. |
| `-count` | `5` | Benchmark runs; each metric is the median. |
| `-benchtime` | `1000x` | Fixed iteration counts keep `B/op` comparable. |
| `-tol-bytes` | `8` | Ignore `B/op` changes below this many bytes. |
| `-tol-pct` | `1` | Ignore `B/op` changes below this percentage. |
| `-top` | `25` | Rows per table, largest changes first. |
| `-fail-on-growth` | `-1` | Exit non-zero past this many bytes of total growth. |
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
