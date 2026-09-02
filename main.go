// Command memci compares the memory cost of a change against its base
// revision and renders a Markdown report of what moved.
//
// It measures two things, both of which are reproducible enough to gate on,
// unlike wall-clock timings:
//
//   - benchmark allocations, from `go test -bench -benchmem`, and
//   - binary size, by way of the bindiff command.
//
// Both revisions are measured in the same job on the same runner with the same
// toolchain, so a difference in the report is a difference in the code. Rows
// whose measurement did not move are dropped: the report is a list of changes,
// not a dashboard.
//
//	memci -base ../base -targets ./cmd/... -bindiff bindiff -o report.md
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// marker is the default HTML comment embedded in the report so a PR commenter
// can find and update its own previous comment instead of posting duplicates.
const defaultMarker = "<!-- memci -->"

type config struct {
	baseDir, headDir string
	baseRef, headRef string
	benchPattern     string
	count            string
	benchtime        string
	testTimeout      string
	targets          string
	tinygo           string
	tinygoTargets    string
	tinygoFlags      string
	kind             string
	bindiff          string
	tolBytes         float64
	tolPct           float64
	top              int
	failOnGrowth     int64
	title            string
	marker           string
	out              string
	verbose          bool
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "memci:", err)
		os.Exit(1)
	}
}

func run() error {
	var cfg config
	flag.StringVar(&cfg.baseDir, "base", "", "Directory holding the base revision's checkout. Required.")
	flag.StringVar(&cfg.headDir, "head", ".", "Directory holding the revision under test.")
	flag.StringVar(&cfg.baseRef, "base-ref", "base", "Label for the base revision in the report.")
	flag.StringVar(&cfg.headRef, "head-ref", "head", "Label for the head revision in the report.")
	flag.StringVar(&cfg.benchPattern, "bench", "./...", "Package pattern to benchmark. Empty skips benchmarks.")
	flag.StringVar(&cfg.count, "count", "5", "Benchmark -count. Metrics are reported as the median of the runs.")
	flag.StringVar(&cfg.benchtime, "benchtime", "1000x", "Benchmark -benchtime. A fixed iteration count keeps B/op comparable between revisions.")
	flag.StringVar(&cfg.testTimeout, "timeout", "20m", "Benchmark -timeout.")
	flag.StringVar(&cfg.targets, "targets", "", "Comma separated package patterns to build and size-profile. Empty skips binary sizes.")
	flag.StringVar(&cfg.tinygo, "tinygo", "", "TinyGo command to build the targets with as well, e.g. \"tinygo\". Empty skips TinyGo.")
	flag.StringVar(&cfg.tinygoTargets, "tinygo-targets", "", "Package patterns to build with TinyGo. Defaults to -targets.")
	flag.StringVar(&cfg.tinygoFlags, "tinygo-flags", "", "Extra flags for tinygo build, e.g. \"-target=pico -opt=z\".")
	flag.StringVar(&cfg.kind, "kind", "package", "bindiff granularity for the size tables: segment, section, symbol, package, file or line.")
	flag.StringVar(&cfg.bindiff, "bindiff", "bindiff", "bindiff command, run from the head directory. May include arguments, e.g. \"go run ./cmd/bindiff\".")
	flag.Float64Var(&cfg.tolBytes, "tol-bytes", 8, "Ignore B/op changes smaller than this many bytes. Allocation counts and binary sizes are always exact.")
	flag.Float64Var(&cfg.tolPct, "tol-pct", 1, "Ignore B/op changes smaller than this percentage.")
	flag.IntVar(&cfg.top, "top", 25, "Show at most this many rows per table, largest changes first. 0 shows all.")
	flag.Int64Var(&cfg.failOnGrowth, "fail-on-growth", -1, "Exit non-zero if total binary growth exceeds this many bytes. Negative disables.")
	flag.StringVar(&cfg.title, "title", "Memory and size report", "Report heading.")
	flag.StringVar(&cfg.marker, "marker", defaultMarker, "HTML marker embedded in the report so a commenter can update in place.")
	flag.StringVar(&cfg.out, "o", "", "Write the report here instead of stdout.")
	flag.BoolVar(&cfg.verbose, "v", false, "Log each command as it runs.")
	flag.Parse()

	if cfg.baseDir == "" {
		flag.Usage()
		return errors.New("-base is required")
	}
	if cfg.benchPattern == "" && len(toolchains(cfg)) == 0 {
		return errors.New("nothing to measure: -bench is empty and no toolchain has targets")
	}

	sections, growths, err := measure(cfg)
	if err != nil {
		return err
	}

	out := os.Stdout
	if cfg.out != "" {
		f, err := os.Create(cfg.out)
		if err != nil {
			return err
		}
		defer f.Close()
		out = f
	}
	subtitle := fmt.Sprintf("`%s` compared against `%s`.", cfg.headRef, cfg.baseRef)
	if err := render(out, cfg.marker, cfg.title, subtitle, sections); err != nil {
		return err
	}

	// The budget is applied per toolchain rather than to the sum: a flash budget
	// and a host binary's download size are different limits that happen to be
	// spelled with the same flag, and adding them together would gate on a
	// number that means nothing.
	for _, g := range growths {
		if cfg.failOnGrowth >= 0 && g.bytes > cfg.failOnGrowth {
			return fmt.Errorf("%s binary size grew by %d bytes, over the %d byte budget", g.toolchain, g.bytes, cfg.failOnGrowth)
		}
	}
	return nil
}

// growth is how much one toolchain's binaries grew in total, which is what the
// -fail-on-growth budget applies to.
type growth struct {
	toolchain string
	bytes     int64
}

// measure runs both halves and returns the report sections along with each
// toolchain's total binary growth.
func measure(cfg config) (sections []section, growths []growth, err error) {
	if tcs := toolchains(cfg); len(tcs) > 0 {
		s, g, err := measureSize(cfg, tcs)
		if err != nil {
			return nil, nil, err
		}
		sections = append(sections, s)
		growths = g
	}
	if cfg.benchPattern != "" {
		s, err := measureBench(cfg)
		if err != nil {
			return nil, nil, err
		}
		sections = append(sections, s...)
	}
	return sections, growths, nil
}

// measureBench benchmarks both revisions and builds the allocation tables.
func measureBench(cfg config) ([]section, error) {
	args := []string{"test",
		"-run=^$", "-bench=.", "-benchmem",
		"-count=" + cfg.count,
		"-benchtime=" + cfg.benchtime,
		"-timeout=" + cfg.testTimeout,
		cfg.benchPattern,
	}
	baseOut, err := goCmd(cfg, cfg.baseDir, args...)
	if err != nil {
		return nil, fmt.Errorf("benchmarking %s: %w", cfg.baseRef, err)
	}
	headOut, err := goCmd(cfg, cfg.headDir, args...)
	if err != nil {
		return nil, fmt.Errorf("benchmarking %s: %w", cfg.headRef, err)
	}

	base, err := parseBench(bytes.NewReader(baseOut))
	if err != nil {
		return nil, err
	}
	head, err := parseBench(bytes.NewReader(headOut))
	if err != nil {
		return nil, err
	}

	module := modulePath(cfg, cfg.headDir)
	byteRows, allocRows := benchRows(base, head)

	// Allocation counts are whole numbers produced by the same code path every
	// run, so any change is real and the tolerance stays at zero. Bytes per op
	// is an average over iterations and moves a little on its own, so it gets
	// the configured slack.
	allocRows = keep(allocRows, tolerance{})
	byteRows = keep(byteRows, tolerance{abs: cfg.tolBytes, pct: cfg.tolPct})
	sortRows(allocRows)
	sortRows(byteRows)
	shorten(allocRows, module)
	shorten(byteRows, module)

	note := ""
	if cfg.tolBytes > 0 || cfg.tolPct > 0 {
		note = fmt.Sprintf("_Changes below %s bytes or %s%% are omitted as measurement noise._",
			trimFloat(cfg.tolBytes), trimFloat(cfg.tolPct))
	}
	allocShown, allocMore := trim(allocRows, cfg.top)
	byteShown, byteMore := trim(byteRows, cfg.top)
	sec := section{
		heading: "Benchmark allocations",
		tables: []table{
			{heading: "allocs/op", group: "Package", item: "Benchmark",
				format: trimFloat, rows: allocShown, omitted: allocMore},
			{heading: "B/op", note: note, group: "Package", item: "Benchmark",
				format: trimFloat, rows: byteShown, omitted: byteMore},
		},
	}
	if len(allocRows) == 0 && len(byteRows) == 0 {
		sec.headline = fmt.Sprintf("No change across %s.", quantity(len(head), "benchmark", "benchmarks"))
	}
	return []section{sec}, nil
}

// measureSize builds each target on both revisions with every toolchain and
// diffs the results with bindiff.
func measureSize(cfg config, tcs []toolchain) (section, []growth, error) {
	dir, err := os.MkdirTemp("", "memci")
	if err != nil {
		return section{}, nil, err
	}
	defer os.RemoveAll(dir)

	sec := section{heading: "Binary size"}
	var growths []growth
	var totalRows []Row // One per binary and toolchain, for the side by side table.
	// The headline names binaries when there is one toolchain and toolchains
	// when there are several; either way it is the sentence someone reads
	// instead of the tables.
	var summaries []string
	binaries := make(map[string]bool)
	for _, tc := range tcs {
		targets, err := mainPackages(cfg, tc)
		if err != nil {
			return section{}, nil, err
		}
		if len(targets) == 0 {
			return section{}, nil, fmt.Errorf("no main packages matched %q for %s", tc.targets, tc.name)
		}

		var total int64
		for _, target := range targets {
			name := path.Base(target)
			binaries[name] = true
			basePath := filepath.Join(dir, tc.name+"-base-"+name)
			headPath := filepath.Join(dir, tc.name+"-head-"+name)
			if err := build(cfg, tc, cfg.baseDir, target, basePath); err != nil {
				return section{}, nil, fmt.Errorf("building %s with %s at %s: %w", target, tc.name, cfg.baseRef, err)
			}
			if err := build(cfg, tc, cfg.headDir, target, headPath); err != nil {
				return section{}, nil, fmt.Errorf("building %s with %s at %s: %w", target, tc.name, cfg.headRef, err)
			}

			raw, err := command(cfg, cfg.headDir, bindiffArgv(cfg, tc, basePath, headPath))
			if err != nil {
				return section{}, nil, fmt.Errorf("diffing %s built with %s: %w", target, tc.name, err)
			}
			rows, sum, err := sizeRows(name, raw)
			if err != nil {
				return section{}, nil, err
			}
			total += sum.Delta
			if len(tcs) == 1 && sum.Delta != 0 {
				summaries = append(summaries, fmt.Sprintf("`%s` %s", name, signedBy(float64(sum.Delta), humanBytes)))
			}
			totalRows = append(totalRows, Row{
				Group: name, Name: tc.name, Unit: "bytes",
				Base: float64(sum.Old), Head: float64(sum.New),
				baseOK: sum.Old != 0, headOK: sum.New != 0,
			})

			rows = keep(rows, tolerance{})
			sortRows(rows)
			if len(rows) == 0 {
				continue
			}
			shown, more := trim(rows, cfg.top)
			sec.tables = append(sec.tables, table{
				// The binary and the toolchain are named in the heading, so the
				// rows need only name the unit of attribution.
				heading: fmt.Sprintf("%s — %s", binaryLabel(name, tc, tcs), signedBy(float64(sum.Delta), humanBytes)),
				item:    cfg.kind,
				format:  humanBytes, rows: shown, omitted: more,
			})
		}
		growths = append(growths, growth{toolchain: tc.name, bytes: total})
		if len(tcs) > 1 && total != 0 {
			summaries = append(summaries, fmt.Sprintf("%s %s", tc.name, signedBy(float64(total), humanBytes)))
		}
	}

	switch {
	case len(summaries) == 0:
		sec.headline = fmt.Sprintf("No change across %s.", quantity(len(binaries), "binary", "binaries"))
	case len(tcs) == 1:
		sec.headline = fmt.Sprintf("Total %s: %s.", signedBy(float64(growths[0].bytes), humanBytes), strings.Join(summaries, ", "))
	default:
		sec.headline = fmt.Sprintf("Total %s.", strings.Join(summaries, ", "))
	}
	// With one toolchain the totals are already in the headline; the table earns
	// its space only when there is a second number to hold against the first.
	if len(tcs) > 1 {
		if t, ok := sideBySide(totalRows, tcs); ok {
			sec.tables = append([]table{t}, sec.tables...)
		}
	}
	return sec, growths, nil
}

// quantity renders an amount with the right noun. A firmware repo often has a
// single command and a single benchmark, and "1 binaries" in a PR comment reads
// like a bug.
func quantity(n int, singular, plural string) string {
	if n == 1 {
		return "1 " + singular
	}
	return fmt.Sprintf("%d %s", n, plural)
}

// mainPackages expands a toolchain's target patterns to the main packages they
// match. Only packages present on both revisions are compared; a binary that
// exists on only one side has nothing to diff against.
//
// The list comes from `go list` even for TinyGo: the module is the same one,
// and a package that go list resolves but tinygo cannot build is better
// reported as a build failure than silently skipped. -tinygo-targets is how a
// repo narrows the set to the commands that are meant to cross-compile.
func mainPackages(cfg config, tc toolchain) ([]string, error) {
	list := func(dir string) (map[string]bool, error) {
		args := append([]string{"list", "-f", "{{if eq .Name \"main\"}}{{.ImportPath}}{{end}}"},
			strings.Split(tc.targets, ",")...)
		out, err := goCmd(cfg, dir, args...)
		if err != nil {
			return nil, err
		}
		set := make(map[string]bool)
		for _, line := range strings.Fields(string(out)) {
			set[line] = true
		}
		return set, nil
	}
	head, err := list(cfg.headDir)
	if err != nil {
		return nil, fmt.Errorf("listing targets at %s: %w", cfg.headRef, err)
	}
	base, err := list(cfg.baseDir)
	if err != nil {
		return nil, fmt.Errorf("listing targets at %s: %w", cfg.baseRef, err)
	}
	var out []string
	for pkg := range head {
		if base[pkg] {
			out = append(out, pkg)
		}
	}
	sort.Strings(out)
	return out, nil
}

// modulePath returns the module path of the checkout, used to shorten package
// names in the report. A failure here is cosmetic, so it is not an error.
func modulePath(cfg config, dir string) string {
	out, err := goCmd(cfg, dir, "list", "-m")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// shorten rewrites package paths in place to their module-relative form.
func shorten(rows []Row, module string) {
	for i := range rows {
		rows[i].Group = trimPkg(rows[i].Group, module)
	}
}

func goCmd(cfg config, dir string, args ...string) ([]byte, error) {
	return command(cfg, dir, append([]string{"go"}, args...))
}

// command runs argv in dir and returns its standard output. Standard error is
// passed through so that build failures and go test diagnostics land in the CI
// log where someone can read them.
func command(cfg config, dir string, argv []string) ([]byte, error) {
	if cfg.verbose {
		fmt.Fprintf(os.Stderr, "memci: (%s) %s\n", dir, strings.Join(argv, " "))
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", strings.Join(argv, " "), err)
	}
	return out, nil
}
