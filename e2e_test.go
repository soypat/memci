package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestEndToEnd runs the real pipeline -- two go builds, two benchmark runs and
// a bindiff invocation -- over the httpsrv fixture, whose head copy allocates
// once more per call and links in encoding/json.
//
// It needs bindiff, which memci does not depend on at build time. Point
// MEMCI_BINDIFF at a copy, or have one on PATH, to run it.
func TestEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("end to end test builds and benchmarks two checkouts")
	}
	bindiff := findBindiff(t)

	cfg := config{
		baseDir: "testdata/httpsrv/base",
		headDir: "testdata/httpsrv/head",
		baseRef: "main", headRef: "pr",
		benchPattern: "./...",
		count:        "3",
		benchtime:    "500x",
		testTimeout:  "5m",
		targets:      ".",
		kind:         "package",
		bindiff:      bindiff,
		tolBytes:     8, tolPct: 1,
		top:          25,
		failOnGrowth: -1,
	}

	sections, growth, err := measure(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if growth <= 0 {
		t.Errorf("growth = %d bytes; head links in encoding/json so it must be larger", growth)
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
	bindiff := findBindiff(t)

	cfg := config{
		baseDir: "testdata/httpsrv/base",
		headDir: "testdata/httpsrv/base",
		baseRef: "main", headRef: "pr",
		benchPattern: "./...",
		count:        "3",
		benchtime:    "500x",
		testTimeout:  "5m",
		targets:      ".",
		kind:         "package",
		bindiff:      bindiff,
		tolBytes:     8, tolPct: 1,
		top:          25,
		failOnGrowth: -1,
	}

	sections, growth, err := measure(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if growth != 0 {
		t.Errorf("growth = %d bytes comparing a tree against itself; the build is not reproducible", growth)
	}
	var b strings.Builder
	if err := render(&b, defaultMarker, "Report", "", sections); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "| base | head |") {
		t.Errorf("comparing a tree against itself produced tables:\n%s", b.String())
	}
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
