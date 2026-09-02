package main

import (
	"bufio"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Metrics holds every sample of one benchmark's memory metrics. With -count>1
// each benchmark reports several times; keeping the samples lets us take a
// median, which is far less jumpy than any single run.
type Metrics struct {
	BytesPerOp  []float64
	AllocsPerOp []float64
}

// gomaxprocsSuffix is the "-8" that go test appends to a benchmark name. It
// records the runner's CPU count, which is incidental to the identity of the
// benchmark, so it is stripped before base and head are matched up.
var gomaxprocsSuffix = regexp.MustCompile(`-\d+$`)

// parseBench reads `go test -bench -benchmem` output and groups metric samples
// by package and benchmark name.
//
// Anything that is not a benchmark result line is ignored, so the same reader
// can be handed a log that also contains build output or test chatter.
func parseBench(r io.Reader) (map[key]*Metrics, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	out := make(map[key]*Metrics)
	var pkg string
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "pkg:" && len(fields) >= 2 {
			pkg = fields[1]
			continue
		}
		if !strings.HasPrefix(fields[0], "Benchmark") || len(fields) < 4 {
			continue
		}
		// A benchmark line is: name iterations (value unit)... The iteration
		// count is what distinguishes it from a log line that merely starts
		// with the word Benchmark.
		if _, err := strconv.Atoi(fields[1]); err != nil {
			continue
		}
		k := key{group: pkg, name: gomaxprocsSuffix.ReplaceAllString(fields[0], "")}
		m := out[k]
		if m == nil {
			m = &Metrics{}
			out[k] = m
		}
		parseMetrics(m, fields[2:])
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// parseMetrics consumes (value, unit) pairs. Metrics memci does not report,
// notably ns/op and MB/s, are skipped: they measure the runner, not the code.
func parseMetrics(m *Metrics, tokens []string) {
	for i := 0; i+1 < len(tokens); i += 2 {
		v, err := strconv.ParseFloat(tokens[i], 64)
		if err != nil {
			continue
		}
		switch tokens[i+1] {
		case "B/op":
			m.BytesPerOp = append(m.BytesPerOp, v)
		case "allocs/op":
			m.AllocsPerOp = append(m.AllocsPerOp, v)
		}
	}
}

// median returns the median of samples. ok is false when there are none, which
// is how a benchmark that reports no memory metrics is left out of a table
// rather than shown as zero.
func median(samples []float64) (value float64, ok bool) {
	if len(samples) == 0 {
		return 0, false
	}
	s := append([]float64(nil), samples...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2], true
	}
	return (s[n/2-1] + s[n/2]) / 2, true
}

// benchRows compares two benchmark logs and returns one set of rows per metric.
// Each metric gets its own table because they answer different questions: an
// allocs/op change is a structural change to the code, while a B/op change is
// often just a differently sized buffer.
func benchRows(base, head map[key]*Metrics) (bytesRows, allocRows []Row) {
	pick := func(ms map[key]*Metrics, get func(*Metrics) []float64) map[key]float64 {
		out := make(map[key]float64, len(ms))
		for k, m := range ms {
			if v, ok := median(get(m)); ok {
				out[k] = v
			}
		}
		return out
	}
	bytesOf := func(m *Metrics) []float64 { return m.BytesPerOp }
	allocsOf := func(m *Metrics) []float64 { return m.AllocsPerOp }

	bytesRows = join("B/op", pick(base, bytesOf), pick(head, bytesOf))
	allocRows = join("allocs/op", pick(base, allocsOf), pick(head, allocsOf))
	return bytesRows, allocRows
}
