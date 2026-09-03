# memci

Reports what a pull request did to your **benchmark allocations** and your
**binary size**, and nothing else. Rows that did not move are dropped, so an
empty report means nothing got worse.

```
# Size and Allocations `add-json-encoding` vs. `main`

Binary size +258.52 KiB: `httpsrv` +258.52 KiB. Allocations +1 allocs/op, +72 B/op across 1 benchmark.

<details>
<summary>Breakdown by package</summary>

**httpsrv — +258.52 KiB**

| package | base | head | Δ |  |
| --- | ---: | ---: | ---: | ---: |
| encoding/json | — | 57.71 KiB | +57.71 KiB | new |
| slices | 68.01 KiB | 101.46 KiB | +33.45 KiB | +49.2% |

</details>

**allocs/op**

| Package | Benchmark | base | head | Δ |  |
| --- | --- | ---: | ---: | ---: | ---: |
| . | BenchmarkGreet | 1 | 2 | +1 | +100.0% |
```

When nothing moved, that is the whole comment:

```
# Size and Allocations `add-json-encoding` vs. `main`

No change across 1 binary and 12 benchmarks.
```

The title, one sentence, then the numbers. What moved and by how much stays in
front of the fold; which package it came from is one click down, so a repo with
a dozen binaries still opens with an answer rather than with a wall of tables.

With several targets the totals sit next to each other, above the same fold.

It deliberately does not report `ns/op`. Timings measure the runner; `B/op`,
`allocs/op` and binary bytes measure the code.

## How it measures

Both revisions are built and benchmarked **in the same job, on the same runner,
with the same toolchain**, by adding a `git worktree` for the base branch. There
is no stored baseline to expire, to be missing on a first run, or to have been
produced by a different Go version.

Binary size comes from [`bindiff`](https://github.com/soypat/tinyboot), which
attributes every byte of an ELF to a segment, section, symbol, package, source
file or line.

## Targets

A target is a **build command** and the ELF it produces. The same command runs
in both checkouts, and the report quotes it verbatim under each table, so what
was measured is never in doubt:

```json
[
  {"name": "cli",
   "build": "go build -trimpath -buildvcs=false -o cli.elf ./cmd/cli",
   "elf": "cli.elf"},
  {"name": "firmware",
   "build": "tinygo build -o fw.elf -target=pico -opt=z ./cmd/fw",
   "elf": "fw.elf",
   "mem": true}
]
```

Taking the command rather than a package pattern is what lets one repo measure
the same package twice — two toolchains, two tag sets, two optimization levels.
A change that is free in a host binary can be expensive on a microcontroller,
one interface method that drags a reflect path into the firmware, and no single
build stands in for both.

`-targets ./cmd/...` is shorthand for one host build per main package, with
`-trimpath -buildvcs=false` already applied. Spell the command out and those
flags are yours to pass: they remove the two things that otherwise differ
between the checkouts for reasons unrelated to the change — the directory the
build ran in, and the stamped commit.

`mem` picks what a target counts. By default it is the **bytes of the file**,
which is what ships; a binary's `.bss` is a virtual reservation nobody pays for,
and the Go runtime alone reserves tens of megabytes of untouched `.noptrbss`
that would swamp the total and dilute every percentage in the table. With `mem`
it is the **loadable image plus `.bss`** (bindiff's `-mem`), because on a device
that is the memory that has to exist. It is also the reproducible measure for a
toolchain without `-trimpath`, such as TinyGo: the checkout path reaches the
DWARF, so two builds of identical source differ by the length of the directory
they were built in, and none of those bytes are loadable. When targets disagree
about this, the totals table says which is which.

Benchmarks are a Go-only measurement, unaffected by the target list.

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
| `args` | | Flags for memci, below. |
| `bindiff` | `github.com/soypat/tinyboot/cmd/bindiff@latest` | Installed if it contains `@`, otherwise run as a command. |
| `targets` | | The build list: a JSON array, or the path of a `.json` file in the repo holding one. Without one, every main package gets a host build. |

Those are the whole surface: `targets` says what to build, `args` says how memci
runs. A build command naming a toolchain — TinyGo, a cross-compiler, anything —
is the workflow's job to install, in a step before this one.

`targets` is an input of its own rather than part of `args` because `args` is
word-split by the shell, which a JSON document does not survive. An action input
is always a string, so an inline list needs a `|` block scalar.

A firmware repo's `.github/workflows/memci.yml`, sizing a host command and the
one that ships to the board:

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
      # Needed only because a build command below names tinygo. Keep the two
      # versions in step: TinyGo 0.42 builds with Go 1.25 through 1.27 and
      # refuses to run outside that window, in either direction.
      - uses: acifani/setup-tinygo@v2
        with:
          tinygo-version: "0.42.0"
      - uses: soypat/memci@v1
        with:
          args: -kind package -bench ./...
          targets: |
            [
              {"name": "cli",
               "build": "go build -trimpath -buildvcs=false -o cli.elf ./cmd/cli",
               "elf": "cli.elf"},
              {"name": "firmware",
               "build": "tinygo build -o fw.elf -target=pico ./cmd/fw",
               "elf": "fw.elf",
               "mem": true}
            ]
```

Once that list grows past a couple of entries it reads better in a file, which
also lets a PR change what is measured in the same commit that needs it:

```yaml
        with:
          targets: .github/memci.json
```

The path resolves against the checkout under test. Watch for a blanket
`*.json` in `.gitignore`; the file has to be committed to exist in CI.

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
| `-targets` | | Package patterns to build with the host toolchain. Shorthand for one `go build` per main package. |
| `-targets-json` | | The build list: a JSON array, or the path of a `.json` file holding one. Combines with `-targets`. |
| `-bench` | `./...` | Package pattern to benchmark. Empty skips benchmarks. |
| `-kind` | `package` | Size granularity: `segment`, `section`, `symbol`, `package`, `file`, `line`. |
| `-count` | `5` | Benchmark runs; each metric is the median. |
| `-benchtime` | `1000x` | Fixed iteration counts keep `B/op` comparable. |
| `-tol-bytes` | `8` | Ignore `B/op` changes below this many bytes. |
| `-tol-pct` | `1` | Ignore `B/op` changes below this percentage. |
| `-top` | `25` | Rows per table, largest changes first. |
| `-fail-on-growth` | `-1` | Exit non-zero past this many bytes of growth, per target. |
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
