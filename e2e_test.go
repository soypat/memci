package main

import (
	"os"
	"os/exec"
	"path/filepath"
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
	if len(growths) != 1 || growths[0].target != "httpsrvfixture" {
		t.Fatalf("growths = %v, want one httpsrvfixture entry", growths)
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
	// With one target there is nothing to hold the total against, so the totals
	// table must not appear.
	if strings.Contains(got, "**Totals**") {
		t.Errorf("a single target produced a totals table:\n%s", got)
	}
	// The build line is the report's record of exactly what was measured.
	if !strings.Contains(got, "`go build -trimpath -buildvcs=false") {
		t.Errorf("report does not show the build command:\n%s", got)
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

// TestEndToEndJSONTargets builds the same package twice under different flags,
// which is what the JSON target list exists for, and checks the two land side
// by side with the command that produced each.
func TestEndToEndJSONTargets(t *testing.T) {
	if testing.Short() {
		t.Skip("end to end test builds two checkouts twice over")
	}
	cfg := fixtureConfig(t)
	cfg.targets = ""      // Only the explicit list below.
	cfg.benchPattern = "" // Benchmarks are unaffected by targets; skip them here.
	cfg.targetsJSON = `[
		{"name": "plain", "build": "go build -trimpath -buildvcs=false -o plain.elf .", "elf": "plain.elf"},
		{"name": "stripped", "build": "go build -trimpath -buildvcs=false -ldflags=-w -o stripped.elf .", "elf": "stripped.elf", "mem": true}
	]`
	cleanELFs(t, cfg, "plain.elf", "stripped.elf")

	sections, growths, err := measure(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(growths) != 2 || growths[0].target != "plain" || growths[1].target != "stripped" {
		t.Fatalf("growths = %v, want plain then stripped in listed order", growths)
	}
	for _, g := range growths {
		if g.bytes <= 0 {
			t.Errorf("%s growth = %d bytes; head links in encoding/json so it must be larger", g.target, g.bytes)
		}
	}

	var b strings.Builder
	if err := render(&b, defaultMarker, "Report", "", sections); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	t.Log("\n" + got)

	for _, want := range []string{
		"**Totals**",                // Two targets earn the side by side table,
		"| plain |", "| stripped |", // one row each.
		"loadable image plus `.bss`",                                         // Which says what it is counting, since they differ.
		"`go build -trimpath -buildvcs=false -ldflags=-w -o stripped.elf .`", // The exact build line.
		"Binary size: `plain` +",                                             // The headline carries both.
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report is missing %q:\n%s", want, got)
		}
	}
	assertRectangular(t, got)
}

// TestJSONTargetsBuildInBothCheckouts pins the trap the two builds share: they
// run the same command, so the head build must not land on the base binary
// before bindiff has read it.
func TestJSONTargetsBuildInBothCheckouts(t *testing.T) {
	if testing.Short() {
		t.Skip("end to end test builds two checkouts")
	}
	cfg := fixtureConfig(t)
	cfg.targets = ""
	cfg.benchPattern = ""
	cfg.targetsJSON = `[{"name": "srv", "build": "go build -trimpath -buildvcs=false -o srv.elf .", "elf": "srv.elf"}]`
	cleanELFs(t, cfg, "srv.elf")

	_, growths, err := measure(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// If the head build had overwritten the base one, the diff would be of a
	// binary against itself and report no change at all.
	if len(growths) != 1 || growths[0].bytes == 0 {
		t.Fatalf("growths = %v; the two checkouts differ so the size must too", growths)
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

// cleanELFs removes binaries a build command left in the fixture checkouts. In
// CI the checkout is disposable; the fixture directories are not.
func cleanELFs(t *testing.T, cfg config, names ...string) {
	t.Helper()
	t.Cleanup(func() {
		for _, dir := range []string{cfg.baseDir, cfg.headDir} {
			for _, name := range names {
				os.Remove(filepath.Join(dir, name))
			}
		}
	})
}
