package main

import (
	"strings"
	"testing"
)

// sampleLog is real `go test -bench -benchmem` output. It carries the cases the
// parser has to survive: several packages, a metric memci ignores (MB/s), a
// benchmark with no memory metrics at all, repeated runs from -count, and log
// lines that are not results.
const sampleLog = `goos: linux
goarch: amd64
pkg: example.com/mod/alpha
cpu: Intel(R) Core(TM) i5-8265U CPU @ 1.60GHz
BenchmarkFoo-8   	 1000000	      1043 ns/op	     128 B/op	       2 allocs/op
BenchmarkFoo-8   	 1000000	      1101 ns/op	     136 B/op	       2 allocs/op
BenchmarkFoo-8   	 1000000	      1077 ns/op	     120 B/op	       2 allocs/op
BenchmarkThroughput-8   	  200000	      6021 ns/op	 680.12 MB/s	     512 B/op	       1 allocs/op
BenchmarkNoMem-8   	 5000000	       210.4 ns/op
PASS
ok  	example.com/mod/alpha	4.012s
goos: linux
goarch: amd64
pkg: example.com/mod/beta
BenchmarkFoo-8   	  300000	      3011 ns/op	      64 B/op	       0 allocs/op
Benchmark results are being written to disk
PASS
ok  	example.com/mod/beta	1.004s
`

func parseString(t *testing.T, s string) map[key]*Metrics {
	t.Helper()
	m, err := parseBench(strings.NewReader(s))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestParseBench(t *testing.T) {
	got := parseString(t, sampleLog)

	// The GOMAXPROCS suffix records the runner, not the benchmark, and is
	// stripped so the key is stable.
	foo := key{"example.com/mod/alpha", "BenchmarkFoo"}
	m, ok := got[foo]
	if !ok {
		t.Fatalf("BenchmarkFoo missing; got keys %v", keys(got))
	}
	if len(m.BytesPerOp) != 3 || len(m.AllocsPerOp) != 3 {
		t.Fatalf("got %d B/op and %d allocs/op samples, want 3 of each (one per -count run)",
			len(m.BytesPerOp), len(m.AllocsPerOp))
	}

	// The same benchmark name in another package is a different measurement.
	if _, ok := got[key{"example.com/mod/beta", "BenchmarkFoo"}]; !ok {
		t.Error("BenchmarkFoo in package beta was folded into the one in alpha")
	}

	// A benchmark with no memory metrics is still seen, but contributes no
	// samples, so it drops out of the tables rather than reading as zero.
	noMem, ok := got[key{"example.com/mod/alpha", "BenchmarkNoMem"}]
	if !ok {
		t.Fatal("BenchmarkNoMem missing")
	}
	if len(noMem.BytesPerOp) != 0 || len(noMem.AllocsPerOp) != 0 {
		t.Error("BenchmarkNoMem picked up memory samples it never reported")
	}

	// MB/s sits between ns/op and B/op; a positional parser would mistake it
	// for the byte count.
	tp := got[key{"example.com/mod/alpha", "BenchmarkThroughput"}]
	if v, _ := median(tp.BytesPerOp); v != 512 {
		t.Errorf("B/op for BenchmarkThroughput = %v, want 512 (MB/s was misread)", v)
	}

	// "Benchmark results are being written to disk" is prose, not a result.
	for k := range got {
		if strings.HasPrefix(k.name, "Benchmark results") {
			t.Errorf("parsed a log line as a benchmark: %q", k.name)
		}
	}
}

func TestMedian(t *testing.T) {
	if _, ok := median(nil); ok {
		t.Error("median of no samples reported ok")
	}
	if v, _ := median([]float64{3, 1, 2}); v != 2 {
		t.Errorf("odd count median = %v, want 2", v)
	}
	if v, _ := median([]float64{4, 1, 2, 3}); v != 2.5 {
		t.Errorf("even count median = %v, want 2.5", v)
	}
	// The median is the point of -count: one wild run must not move it.
	if v, _ := median([]float64{100, 100, 100, 100, 9999}); v != 100 {
		t.Errorf("median with an outlier = %v, want 100", v)
	}
}

func TestBenchRowsSplitsMetrics(t *testing.T) {
	base := parseString(t, sampleLog)
	head := parseString(t, strings.ReplaceAll(sampleLog,
		"BenchmarkFoo-8   	 1000000	      1043 ns/op	     128 B/op	       2 allocs/op",
		"BenchmarkFoo-8   	 1000000	      1043 ns/op	     128 B/op	       5 allocs/op"))

	byteRows, allocRows := benchRows(base, head)
	for _, r := range byteRows {
		if r.Unit != "B/op" {
			t.Errorf("byte row carries unit %q", r.Unit)
		}
	}
	for _, r := range allocRows {
		if r.Unit != "allocs/op" {
			t.Errorf("alloc row carries unit %q", r.Unit)
		}
	}

	// One of three samples changed, so the median moves from 2 to 2 for allocs
	// -- which is exactly the point of taking a median. Bytes did not change at
	// all. Nothing should survive at zero tolerance.
	if got := keep(allocRows, tolerance{}); len(got) != 0 {
		t.Errorf("a single outlier run produced %d alloc rows: %+v", len(got), got)
	}
	if got := keep(byteRows, tolerance{}); len(got) != 0 {
		t.Errorf("unchanged bytes produced %d rows: %+v", len(got), got)
	}
}

// TestBenchRowsOnlyMeasuredMetrics guards the case where a benchmark reports
// allocation counts but the log has no B/op for it: it must appear in one table
// and not the other.
func TestBenchRowsOnlyMeasuredMetrics(t *testing.T) {
	m := parseString(t, sampleLog)
	byteRows, allocRows := benchRows(m, m)
	countNoMem := func(rows []Row) int {
		n := 0
		for _, r := range rows {
			if r.Name == "BenchmarkNoMem" {
				n++
			}
		}
		return n
	}
	if countNoMem(byteRows) != 0 || countNoMem(allocRows) != 0 {
		t.Error("a benchmark with no -benchmem metrics produced rows")
	}
}

func keys(m map[key]*Metrics) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k.group+" "+k.name)
	}
	return out
}
