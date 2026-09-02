package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// fixtureConfig is the pipeline pointed at the httpsrv fixture, whose head copy
// allocates once more per call and links in encoding/json.
func fixtureConfig(t *testing.T) config {
	t.Helper()
	return config{
		baseDir: "testdata/httpsrv/base",
		headDir: "testdata/httpsrv/head",
		baseRef: "main", headRef: "pr",
		benchPattern: "./...",
		count:        "3",
		benchtime:    "500x",
		testTimeout:  "5m",
		targets:      ".",
		kind:         "package",
		bindiff:      findBindiff(t),
		tolBytes:     8, tolPct: 1,
		top:          25,
		failOnGrowth: -1,
	}
}

// TestEndToEnd runs the real pipeline -- two go builds, two benchmark runs and
// a bindiff invocation -- over the httpsrv fixture.
//
// It needs bindiff, which memci does not depend on at build time. Point
// MEMCI_BINDIFF at a copy, or have one on PATH, to run it.
func TestEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("end to end test builds and benchmarks two checkouts")
	}
	cfg := fixtureConfig(t)

	sections, growths, err := measure(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(growths) != 1 || growths[0].toolchain != "go" {
		t.Fatalf("growths = %v, want one go entry", growths)
	}
	if growths[0].bytes <= 0 {
		t.Errorf("growth = %d bytes; head links in encoding/json so it must be larger", growths[0].bytes)
	}

	var b strings.Builder
	if err := render(&b, defaultMarker, "Report", "", sections); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	t.Log("\n" + got)

	for _, want := range []string{
		"encoding/json",  // Only head imports it, so it is a new package.
		"BenchmarkGreet", // 1 -> 2 allocs/op.
		"allocs/op",      // Which means the allocation table exists.
		"httpsrvfixture", // The binary is named.
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report is missing %q:\n%s", want, got)
		}
	}
	// With one toolchain there is nothing to hold the totals against, so the
	// side by side table must not appear.
	if strings.Contains(got, "**Totals**") {
		t.Errorf("a single toolchain produced a totals table:\n%s", got)
	}

	// BenchmarkStable is byte for byte identical on both sides. If it shows up,
	// the "only what moved" rule is broken -- which is the whole report.
	if strings.Contains(got, "BenchmarkStable") {
		t.Errorf("an unchanged benchmark appeared in the report:\n%s", got)
	}
	assertRectangular(t, got)
}

// TestEndToEndNoChange compares the base fixture against itself. Two builds of
// identical source with -trimpath and -buildvcs=false are byte identical, so
// the report must be empty rather than full of near-zero rows.
func TestEndToEndNoChange(t *testing.T) {
	if testing.Short() {
		t.Skip("end to end test builds and benchmarks two checkouts")
	}
	cfg := fixtureConfig(t)
	cfg.headDir = cfg.baseDir

	sections, growths, err := measure(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if growths[0].bytes != 0 {
		t.Errorf("growth = %d bytes comparing a tree against itself; the build is not reproducible", growths[0].bytes)
	}
	var b strings.Builder
	if err := render(&b, defaultMarker, "Report", "", sections); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "| base | head |") {
		t.Errorf("comparing a tree against itself produced tables:\n%s", b.String())
	}
}

// TestEndToEndTinyGo builds the same fixture with both toolchains and checks
// the two totals end up next to each other.
//
// TinyGo is slow -- around half a minute per build, so two minutes for the four
// here -- and is not needed by the rest of the suite, so this runs only when a
// tinygo is actually present.
func TestEndToEndTinyGo(t *testing.T) {
	if testing.Short() {
		t.Skip("end to end test builds two checkouts with two toolchains")
	}
	tinygo := findTinyGo(t)
	cfg := fixtureConfig(t)
	cfg.tinygo = tinygo
	cfg.benchPattern = "" // Benchmarks are a go-only measurement; skip them here.

	sections, growths, err := measure(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(growths) != 2 || growths[0].toolchain != "go" || growths[1].toolchain != "tinygo" {
		t.Fatalf("growths = %v, want go then tinygo", growths)
	}
	for _, g := range growths {
		if g.bytes <= 0 {
			t.Errorf("%s growth = %d bytes; head links in encoding/json so it must be larger", g.toolchain, g.bytes)
		}
	}

	var b strings.Builder
	if err := render(&b, defaultMarker, "Report", "", sections); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	t.Log("\n" + got)

	for _, want := range []string{
		"**Totals**",
		"| httpsrvfixture | go |",     // The two toolchains are rows of one table,
		"| httpsrvfixture | tinygo |", // adjacent, which is the point of it.
		"loadable image plus `.bss`",  // And the table says what it is counting.
		"httpsrvfixture (go) —",       // Detail tables are qualified once there are two.
		"httpsrvfixture (tinygo) —",
		"Total go +", "tinygo +", // The headline carries both totals.
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report is missing %q:\n%s", want, got)
		}
	}
	assertRectangular(t, got)
}

func findBindiff(t *testing.T) string {
	t.Helper()
	if cmd := os.Getenv("MEMCI_BINDIFF"); cmd != "" {
		return cmd
	}
	path, err := exec.LookPath("bindiff")
	if err != nil {
		t.Skip("bindiff not found; set MEMCI_BINDIFF or install github.com/soypat/tinyboot/cmd/bindiff")
	}
	return path
}

func findTinyGo(t *testing.T) string {
	t.Helper()
	if cmd := os.Getenv("MEMCI_TINYGO"); cmd != "" {
		return cmd
	}
	path, err := exec.LookPath("tinygo")
	if err != nil {
		t.Skip("tinygo not found; set MEMCI_TINYGO or install tinygo")
	}
	return path
}
