package main

import (
	"strings"
	"testing"
)

func renderString(t *testing.T, sections []section) string {
	t.Helper()
	var b strings.Builder
	if err := render(&b, defaultMarker, "Report", "`pr` compared against `main`.", sections); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestRenderEmptyTablesAreOmitted(t *testing.T) {
	// The whole point of the report is that it lists changes. A section whose
	// tables are all empty and that has nothing to say must not appear.
	got := renderString(t, []section{{
		heading: "Benchmark allocations",
		tables: []table{
			{heading: "allocs/op", item: "Benchmark", format: trimFloat},
			{heading: "B/op", item: "Benchmark", format: trimFloat},
		},
	}})
	if strings.Contains(got, "allocs/op") || strings.Contains(got, "Benchmark allocations") {
		t.Errorf("an empty section was rendered:\n%s", got)
	}
	if !strings.Contains(got, "No change in binary size or benchmark allocations.") {
		t.Errorf("a report with nothing in it does not say so:\n%s", got)
	}
}

func TestRenderHeadlineSurvivesEmptyTables(t *testing.T) {
	// "Nothing changed" is a result worth stating, so a section with a headline
	// renders even with no rows under it.
	got := renderString(t, []section{{
		heading:  "Binary size",
		headline: "No change across 2 binaries.",
		tables:   []table{{item: "package", format: humanBytes}},
	}})
	if !strings.Contains(got, "No change across 2 binaries.") {
		t.Errorf("the headline was dropped:\n%s", got)
	}
	if strings.Contains(got, "| base |") {
		t.Errorf("an empty table was still rendered:\n%s", got)
	}
}

func TestRenderMarkerIsFirst(t *testing.T) {
	// The commenter finds its previous comment by this marker; if it moves or
	// disappears every push posts a new comment.
	got := renderString(t, nil)
	if !strings.HasPrefix(got, defaultMarker) {
		t.Errorf("report does not start with the marker:\n%s", got)
	}
}

func TestRenderSizeTable(t *testing.T) {
	rows := []Row{
		{Group: "httpsrv", Name: "encoding/json", Unit: "bytes", Head: 59092, headOK: true},
		{Group: "httpsrv", Name: "runtime", Unit: "bytes", Base: 501482, Head: 502301, baseOK: true, headOK: true},
		{Group: "httpsrv", Name: "old/pkg", Unit: "bytes", Base: 4096, baseOK: true},
	}
	got := renderString(t, []section{{
		heading:  "Binary size",
		headline: "Total +54.62 KiB.",
		tables: []table{{
			heading: "httpsrv — +54.62 KiB",
			item:    "package", format: humanBytes, rows: rows, omitted: 3,
		}},
	}})

	// A size table names its binary in the heading, so repeating it in every
	// row would be noise; the grouping column is dropped.
	if strings.Contains(got, "| httpsrv | encoding/json |") {
		t.Errorf("the redundant grouping column was rendered:\n%s", got)
	}
	for _, want := range []string{
		"| package | base | head | Δ |  |",
		"| encoding/json | — | 57.71 KiB | +57.71 KiB | new |",
		"| old/pkg | 4 KiB | — | -4 KiB | gone |",
		"_3 smaller change(s) not shown._",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing line %q in:\n%s", want, got)
		}
	}
	// Every row must have the same number of cells as the header, or GitHub
	// silently mangles the table.
	assertRectangular(t, got)
}

func TestRenderBenchTableKeepsGroupColumn(t *testing.T) {
	// Benchmarks from different packages share one table, so the package has to
	// stay visible.
	got := renderString(t, []section{{
		heading: "Benchmark allocations",
		tables: []table{{
			heading: "allocs/op", group: "Package", item: "Benchmark", format: trimFloat,
			rows: []Row{{Group: "filesystem", Name: "BenchmarkStat", Base: 1, Head: 3, baseOK: true, headOK: true}},
		}},
	}})
	if !strings.Contains(got, "| Package | Benchmark | base | head | Δ |  |") {
		t.Errorf("the grouping column was dropped from a benchmark table:\n%s", got)
	}
	if !strings.Contains(got, "| filesystem | BenchmarkStat | 1 | 3 | +2 | +200.0% |") {
		t.Errorf("unexpected benchmark row in:\n%s", got)
	}
	assertRectangular(t, got)
}

func TestRenderEscapesPipes(t *testing.T) {
	// Go type and symbol names contain pipes often enough to matter, and an
	// unescaped one silently splits the row into an extra column.
	got := renderString(t, []section{{
		heading: "Binary size",
		tables: []table{{
			item: "symbol", format: humanBytes,
			rows: []Row{{Name: "pkg.fn[chan|int]", Base: 10, Head: 20, baseOK: true, headOK: true}},
		}},
	}})
	if !strings.Contains(got, `pkg.fn[chan\|int]`) {
		t.Errorf("a pipe in a name was not escaped:\n%s", got)
	}
	assertRectangular(t, got)
}

// assertRectangular checks every Markdown table in s has a constant column
// count. GitHub drops cells past the header width without complaining, so a
// mismatch loses data in the rendered comment.
func assertRectangular(t *testing.T, s string) {
	t.Helper()
	want, inTable := 0, false
	for i, line := range strings.Split(s, "\n") {
		if !strings.HasPrefix(line, "|") {
			inTable = false
			continue
		}
		// An escaped pipe is cell content, not a separator.
		n := strings.Count(strings.ReplaceAll(line, `\|`, ""), "|")
		if !inTable {
			want, inTable = n, true
			continue
		}
		if n != want {
			t.Errorf("line %d has %d column separators, want %d:\n\t%s", i+1, n, want, line)
		}
	}
}
