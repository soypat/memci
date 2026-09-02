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
	// The whole point of the report is that it lists changes. Tables with no
	// rows are not rendered, and a report of nothing but those says so in one
	// line instead of printing empty structure.
	got := renderString(t, []section{{
		scope:    "5 benchmarks",
		headline: "Allocations unchanged.",
		tables: []table{
			{heading: "allocs/op", item: "Benchmark", format: trimFloat},
			{heading: "B/op", item: "Benchmark", format: trimFloat},
		},
	}})
	if strings.Contains(got, "allocs/op") || strings.Contains(got, "|") {
		t.Errorf("an empty section was rendered:\n%s", got)
	}
	if !strings.Contains(got, "No change across 5 benchmarks.") {
		t.Errorf("a report with nothing in it does not say so:\n%s", got)
	}
}

func TestRenderNothingChangedNamesWhatWasMeasured(t *testing.T) {
	// "Nothing moved" and "nothing was measured" look identical from the
	// outside, so the one line the report does print distinguishes them. The
	// per-half clauses are dropped: repeating "unchanged" twice says less.
	got := renderString(t, []section{
		{scope: "1 binary", headline: "Binary size unchanged."},
		{scope: "5 benchmarks", headline: "Allocations unchanged."},
	})
	if !strings.Contains(got, "No change across 1 binary and 5 benchmarks.") {
		t.Errorf("the report does not say what it measured:\n%s", got)
	}
	if strings.Contains(got, "Binary size unchanged.") {
		t.Errorf("the per-half clauses were repeated under the summary:\n%s", got)
	}
	// And with nothing measured at all there is still a sentence.
	if got := renderString(t, nil); !strings.Contains(got, "No change.") {
		t.Errorf("an empty report says nothing:\n%s", got)
	}
}

func TestRenderHeadlinesShareOneParagraph(t *testing.T) {
	// Both halves report in a single sentence pair under the title, in section
	// order, with the tables following underneath.
	rows := []Row{{Name: "runtime", Base: 10, Head: 20, baseOK: true, headOK: true}}
	got := renderString(t, []section{
		{scope: "1 binary", headline: "Binary size +193 B: `blinky` +193 B.",
			tables: []table{{heading: "blinky — +193 B", item: "package", format: humanBytes, rows: rows}}},
		{scope: "5 benchmarks", headline: "Allocations +2 B/op across 1 benchmark."},
	})
	want := "Binary size +193 B: `blinky` +193 B. Allocations +2 B/op across 1 benchmark.\n\n"
	if !strings.Contains(got, want) {
		t.Errorf("the halves do not share one paragraph:\n%s", got)
	}
	if strings.Index(got, want) > strings.Index(got, "| package |") {
		t.Errorf("the paragraph does not come before the tables:\n%s", got)
	}
}

func TestRenderFoldsDetailsAway(t *testing.T) {
	rows := []Row{{Group: "httpsrv", Name: "encoding/json", Unit: "bytes", Head: 59092, headOK: true}}
	got := renderString(t, []section{{
		headline: "Total go +54.62 KiB, tinygo +1.2 KiB.",
		tables: []table{{
			heading: "Totals", group: "binary", item: "toolchain", format: humanBytes,
			rows: []Row{{Group: "httpsrv", Name: "go", Base: 100, Head: 200, baseOK: true, headOK: true}},
		}},
		details: []table{
			{heading: "httpsrv (go) — +57.71 KiB", item: "package", format: humanBytes, rows: rows},
			{heading: "httpsrv (tinygo) — +1.2 KiB", item: "package", format: humanBytes, rows: rows},
		},
		detailsSummary: "Breakdown by package",
	}})

	// The headline and the totals stay in front of the fold; everything that
	// explains them goes behind it, in one block rather than one per binary.
	head, tail, found := strings.Cut(got, "<details>")
	if !found {
		t.Fatalf("no details element:\n%s", got)
	}
	if !strings.Contains(head, "Total go +54.62 KiB") || !strings.Contains(head, "| binary | toolchain |") {
		t.Errorf("the headline or the totals table was folded away:\n%s", head)
	}
	if strings.Count(got, "<details>") != 1 || strings.Count(got, "</details>") != 1 {
		t.Errorf("want exactly one details element:\n%s", got)
	}
	for _, want := range []string{"httpsrv (go) — +57.71 KiB", "httpsrv (tinygo) — +1.2 KiB"} {
		if !strings.Contains(tail, want) {
			t.Errorf("breakdown %q is not inside the details:\n%s", want, tail)
		}
	}
	// GitHub renders Markdown in a details block only when blank lines separate
	// it from the tags. Without these the tables show up as literal pipes.
	if !strings.Contains(got, "<summary>Breakdown by package</summary>\n\n") {
		t.Errorf("no blank line after the summary:\n%s", got)
	}
	if !strings.Contains(got, "\n\n</details>") {
		t.Errorf("no blank line before the closing tag:\n%s", got)
	}
	assertRectangular(t, got)
}

func TestRenderDetailsOnlySection(t *testing.T) {
	// A section whose only content is folded away still has to render: the old
	// rule looked at the visible tables alone and would have dropped it.
	got := renderString(t, []section{{
		details: []table{{heading: "httpsrv", item: "package", format: humanBytes,
			rows: []Row{{Name: "runtime", Base: 10, Head: 20, baseOK: true, headOK: true}}}},
	}})
	if !strings.Contains(got, "<details>") || !strings.Contains(got, "| runtime |") {
		t.Errorf("a section with only folded tables was dropped:\n%s", got)
	}
	// And with no summary of its own it still gets a usable label.
	if !strings.Contains(got, "<summary>Breakdown</summary>") {
		t.Errorf("no default summary label:\n%s", got)
	}
}

func TestRenderEmptyDetailsAreOmitted(t *testing.T) {
	// Tables with no rows render nothing, so the details element they would have
	// gone into must not appear empty.
	got := renderString(t, []section{{
		headline: "No change across 2 binaries.",
		details:  []table{{heading: "httpsrv", item: "package", format: humanBytes}},
	}})
	if strings.Contains(got, "<details>") {
		t.Errorf("an empty details element was rendered:\n%s", got)
	}
}

func TestReportTitle(t *testing.T) {
	// The default names both revisions, so a reader scanning a PR's comments can
	// tell what this one is about without opening anything.
	title, subtitle := reportTitle("", "feature/json", "main")
	if want := "Size and Allocations `feature/json` vs. `main`"; title != want {
		t.Errorf("title = %q, want %q", title, want)
	}
	if subtitle != "" {
		t.Errorf("the default title already names both revisions, but got subtitle %q", subtitle)
	}
	// A chosen title says whatever it says, so the revisions need their own line.
	title, subtitle = reportTitle("Nightly size check", "feature/json", "main")
	if title != "Nightly size check" {
		t.Errorf("a custom title was overridden: %q", title)
	}
	if want := "`feature/json` compared against `main`."; subtitle != want {
		t.Errorf("subtitle = %q, want %q", subtitle, want)
	}
}

func TestRenderTitleIsTopLevel(t *testing.T) {
	var b strings.Builder
	title, subtitle := reportTitle("", "pr", "main")
	if err := render(&b, defaultMarker, title, subtitle, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "\n# Size and Allocations `pr` vs. `main`\n") {
		t.Errorf("the title is not a top level heading:\n%s", b.String())
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
