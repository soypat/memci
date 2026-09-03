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
	targetsJSON      string
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
	flag.StringVar(&cfg.targets, "targets", "", "Comma separated package patterns to build with the host toolchain and size-profile.")
	flag.StringVar(&cfg.targetsJSON, "targets-json", "", "JSON array of builds to size-profile, or the path of a .json file holding one. Each entry has build, elf and optionally name and mem.")
	flag.StringVar(&cfg.kind, "kind", "package", "bindiff granularity for the size tables: segment, section, symbol, package, file or line.")
	flag.StringVar(&cfg.bindiff, "bindiff", "bindiff", "bindiff command, run from the head directory. May include arguments, e.g. \"go run ./cmd/bindiff\".")
	flag.Float64Var(&cfg.tolBytes, "tol-bytes", 8, "Ignore B/op changes smaller than this many bytes. Allocation counts and binary sizes are always exact.")
	flag.Float64Var(&cfg.tolPct, "tol-pct", 1, "Ignore B/op changes smaller than this percentage.")
	flag.IntVar(&cfg.top, "top", 25, "Show at most this many rows per table, largest changes first. 0 shows all.")
	flag.Int64Var(&cfg.failOnGrowth, "fail-on-growth", -1, "Exit non-zero if total binary growth exceeds this many bytes. Negative disables.")
	flag.StringVar(&cfg.title, "title", "", "Report heading. The default names the two revisions being compared.")
	flag.StringVar(&cfg.marker, "marker", defaultMarker, "HTML marker embedded in the report so a commenter can update in place.")
	flag.StringVar(&cfg.out, "o", "", "Write the report here instead of stdout.")
	flag.BoolVar(&cfg.verbose, "v", false, "Log each command as it runs.")
	flag.Parse()

	if cfg.baseDir == "" {
		flag.Usage()
		return errors.New("-base is required")
	}
	if cfg.benchPattern == "" && cfg.targets == "" && cfg.targetsJSON == "" {
		return errors.New("nothing to measure: -bench, -targets and -targets-json are all empty")
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
	title, subtitle := reportTitle(cfg.title, cfg.headRef, cfg.baseRef)
	if err := render(out, cfg.marker, title, subtitle, sections); err != nil {
		return err
	}

	// The budget is applied per target rather than to the sum: a flash budget
	// and a host binary's download size are different limits that happen to be
	// spelled with the same flag, and adding them together would gate on a
	// number that means nothing.
	for _, g := range growths {
		if cfg.failOnGrowth >= 0 && g.bytes > cfg.failOnGrowth {
			return fmt.Errorf("%s grew by %d bytes, over the %d byte budget", g.target, g.bytes, cfg.failOnGrowth)
		}
	}
	return nil
}

// growth is how much one target grew, which is what -fail-on-growth applies to.
type growth struct {
	target string
	bytes  int64
}

// measure runs both halves and returns the report sections along with each
// target's binary growth.
func measure(cfg config) (sections []section, growths []growth, err error) {
	if cfg.targets != "" || cfg.targetsJSON != "" {
		// The sugar builds into this directory, and every target's base binary
		// is stashed here before the head build overwrites it.
		dir, err := os.MkdirTemp("", "memci")
		if err != nil {
			return nil, nil, err
		}
		defer os.RemoveAll(dir)

		targets, err := targetsFor(cfg, dir)
		if err != nil {
			return nil, nil, err
		}
		s, g, err := measureSize(cfg, targets, dir)
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

// targetsFor assembles the build list from both spellings. The sugar comes
// first so that -targets and -targets-json can be combined.
func targetsFor(cfg config, outDir string) ([]target, error) {
	var out []target
	if cfg.targets != "" {
		sugar, err := sugarTargets(cfg, outDir)
		if err != nil {
			return nil, err
		}
		out = append(out, sugar...)
	}
	if cfg.targetsJSON != "" {
		raw, err := loadBuilds(cfg)
		if err != nil {
			return nil, err
		}
		explicit, err := parseTargets(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, explicit...)
	}
	return out, uniqueNames(out)
}

// loadBuilds returns the build list JSON, read from a file when -targets-json
// names one rather than carrying the document inline.
//
// The path resolves against the head checkout, so the list travels with the
// code: a PR that adds a command adds its target in the same commit, and both
// revisions are then built the way that PR asks for.
func loadBuilds(cfg config) (string, error) {
	spec := strings.TrimSpace(cfg.targetsJSON)
	if strings.HasPrefix(spec, "[") || !strings.HasSuffix(spec, ".json") {
		return spec, nil
	}
	path := spec
	if !filepath.IsAbs(path) {
		path = filepath.Join(cfg.headDir, path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading the build list: %w", err)
	}
	return string(b), nil
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
		scope: quantity(len(head), "benchmark", "benchmarks"),
		tables: []table{
			{heading: "allocs/op", group: "Package", item: "Benchmark",
				format: trimFloat, rows: allocShown, omitted: allocMore},
			{heading: "B/op", note: note, group: "Package", item: "Benchmark",
				format: trimFloat, rows: byteShown, omitted: byteMore},
		},
	}
	sec.headline = allocHeadline(allocRows, byteRows)
	return []section{sec}, nil
}

// allocHeadline states what the benchmarks did in one clause.
//
// The two figures are the net across the benchmarks that moved, which is a
// severity indicator rather than a measurement of anything -- bytes per op do
// not add up across different benchmarks -- so the sentence says how many
// benchmarks it is summing over and the table below gives them one by one.
func allocHeadline(allocRows, byteRows []Row) string {
	changed := make(map[key]bool)
	var netAllocs, netBytes float64
	for _, r := range allocRows {
		changed[key{r.Group, r.Name}] = true
		netAllocs += r.Delta()
	}
	for _, r := range byteRows {
		changed[key{r.Group, r.Name}] = true
		netBytes += r.Delta()
	}
	if len(changed) == 0 {
		return "Allocations unchanged."
	}
	var parts []string
	if netAllocs != 0 {
		parts = append(parts, signedBy(netAllocs, trimFloat)+" allocs/op")
	}
	if netBytes != 0 {
		parts = append(parts, signedBy(netBytes, trimFloat)+" B/op")
	}
	across := quantity(len(changed), "benchmark", "benchmarks")
	if len(parts) == 0 {
		// Rises and falls that cancel out. The net is zero and the code still
		// moved, so the count is the whole of what there is to say.
		return fmt.Sprintf("Allocations changed across %s.", across)
	}
	return fmt.Sprintf("Allocations %s across %s.", strings.Join(parts, ", "), across)
}

// measureSize builds each target on both revisions and diffs the results with
// bindiff.
func measureSize(cfg config, targets []target, dir string) (section, []growth, error) {
	if len(targets) == 0 {
		return section{}, nil, errors.New("no targets to build")
	}
	var sec section
	var growths []growth
	var totalRows []Row // One per target, for the totals table.
	var summaries []string
	for i, t := range targets {
		if err := build(cfg, t, cfg.baseDir); err != nil {
			return section{}, nil, fmt.Errorf("building %s at %s: %w", t.Name, cfg.baseRef, err)
		}
		basePath := filepath.Join(dir, fmt.Sprintf("base-%d-%s", i, filepath.Base(t.ELF)))
		if err := stash(t.elfPath(cfg.baseDir), basePath); err != nil {
			return section{}, nil, fmt.Errorf("%s at %s: %w", t.Name, cfg.baseRef, err)
		}
		if err := build(cfg, t, cfg.headDir); err != nil {
			return section{}, nil, fmt.Errorf("building %s at %s: %w", t.Name, cfg.headRef, err)
		}

		raw, err := command(cfg, cfg.headDir, bindiffArgv(cfg, t, basePath, t.elfPath(cfg.headDir)))
		if err != nil {
			return section{}, nil, fmt.Errorf("diffing %s: %w", t.Name, err)
		}
		rows, sum, err := sizeRows(t.Name, raw)
		if err != nil {
			return section{}, nil, err
		}
		growths = append(growths, growth{target: t.Name, bytes: sum.Delta})
		if sum.Delta != 0 {
			summaries = append(summaries, fmt.Sprintf("`%s` %s", t.Name, signedBy(float64(sum.Delta), humanBytes)))
		}
		totalRows = append(totalRows, Row{
			Name: t.Name, Unit: "bytes",
			Base: float64(sum.Old), Head: float64(sum.New),
			baseOK: sum.Old != 0, headOK: sum.New != 0,
		})

		rows = keep(rows, tolerance{})
		sortRows(rows)
		if len(rows) == 0 {
			continue
		}
		shown, more := trim(rows, cfg.top)
		sec.details = append(sec.details, table{
			// The target is named in the heading and the build line below it, so
			// the rows need only name the unit of attribution.
			heading: fmt.Sprintf("%s — %s", t.Name, signedBy(float64(sum.Delta), humanBytes)),
			note:    commandNote(t),
			item:    cfg.kind,
			format:  humanBytes, rows: shown, omitted: more,
		})
	}

	sec.scope = quantity(len(targets), "binary", "binaries")
	switch {
	case len(summaries) == 0:
		sec.headline = "Binary size unchanged."
	case len(targets) == 1:
		sec.headline = fmt.Sprintf("Binary size %s.", signedBy(float64(growths[0].bytes), humanBytes))
	default:
		sec.headline = fmt.Sprintf("Binary size: %s.", strings.Join(summaries, ", "))
	}
	if t, ok := totalsTable(totalRows, targets); ok {
		sec.tables = append(sec.tables, t)
	}
	sec.detailsSummary = fmt.Sprintf("Breakdown by %s", cfg.kind)
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

// mainPackages expands the -targets patterns to the main packages they match.
// Only packages present on both revisions are compared; a binary that exists on
// only one side has nothing to diff against.
func mainPackages(cfg config) ([]string, error) {
	list := func(dir string) (map[string]bool, error) {
		args := append([]string{"list", "-f", "{{if eq .Name \"main\"}}{{.ImportPath}}{{end}}"},
			strings.Split(cfg.targets, ",")...)
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
