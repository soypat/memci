package main

import (
	"fmt"
	"io"
	"strings"
)

// table is one rendered section of the report: a heading, an optional note, and
// the rows that changed. A table with no rows is not rendered at all, which is
// what keeps the report to just the things that moved.
type table struct {
	heading string
	note    string
	group   string // Header for the grouping column. Empty omits the column.
	item    string // Header for the item column.
	format  func(float64) string
	rows    []Row
	omitted int // Rows trimmed by -top, mentioned in a footer so the trim is visible.
}

// section holds the tables for one half of the report plus the sentence that
// stands in for them.
//
// A section has no heading of its own: the report is short enough that a
// heading per half is a level of structure it does not need, and the tables
// name themselves.
type section struct {
	// headline is this half's clause of the paragraph under the title, stated
	// even when nothing moved -- "the binary did not change size" is a result.
	headline string
	// scope is what the half measured, e.g. "2 binaries". It is used only when
	// nothing anywhere changed, where the difference between "this change is
	// free" and "the job measured nothing" is worth a few words.
	scope  string
	tables []table

	// details holds the tables folded away behind a summary. What moved and by
	// how much is the answer; which package it came from is the explanation,
	// and a comment that opens with twenty package tables buries the answer in
	// it. Everything is still there, one click down.
	details        []table
	detailsSummary string
}

// reportTitle names the report and, when the name does not already say it, the
// line stating what was compared against what.
//
// The default title carries both revisions because the report is read as one
// comment among many on a busy PR, and "which branch is this about" is the
// first question it has to answer. A title someone chose with -title makes no
// such promise, so the revisions keep their own line underneath it.
func reportTitle(title, headRef, baseRef string) (string, string) {
	if title != "" {
		return title, fmt.Sprintf("`%s` compared against `%s`.", headRef, baseRef)
	}
	return fmt.Sprintf("Size and Allocations `%s` vs. `%s`", headRef, baseRef), ""
}

// render writes the whole Markdown report. marker is an HTML comment that lets
// a PR commenter find and update its previous comment rather than posting a new
// one each push.
func render(w io.Writer, marker, title, subtitle string, sections []section) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n# %s\n\n", marker, title)
	if subtitle != "" {
		fmt.Fprintf(&b, "%s\n\n", subtitle)
	}

	// The halves are rendered first so that the paragraph under the title can
	// say something different when none of them found anything.
	var body strings.Builder
	var headlines, scopes []string
	for _, s := range sections {
		s.render(&body)
		if s.headline != "" {
			headlines = append(headlines, s.headline)
		}
		if s.scope != "" {
			scopes = append(scopes, s.scope)
		}
	}
	switch {
	case body.Len() == 0 && len(scopes) > 0:
		fmt.Fprintf(&b, "No change across %s.\n", strings.Join(scopes, " and "))
	case body.Len() == 0:
		b.WriteString("No change.\n")
	default:
		if len(headlines) > 0 {
			fmt.Fprintf(&b, "%s\n\n", strings.Join(headlines, " "))
		}
		b.WriteString(body.String())
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// render writes a section's tables, folded ones included. The headline is the
// caller's business: every half contributes one clause to a single paragraph
// rather than a block of its own.
func (s section) render(b *strings.Builder) {
	var body, folded strings.Builder
	for _, t := range s.tables {
		t.render(&body)
	}
	for _, t := range s.details {
		t.render(&folded)
	}
	if body.Len() == 0 && folded.Len() == 0 {
		return
	}
	b.WriteString(body.String())
	if folded.Len() > 0 {
		summary := s.detailsSummary
		if summary == "" {
			summary = "Breakdown"
		}
		// The blank lines around the body are load bearing: GitHub renders the
		// Markdown inside a <details> only when it is separated from the tags,
		// and without them the tables arrive as literal pipes.
		fmt.Fprintf(b, "<details>\n<summary>%s</summary>\n\n%s</details>\n\n", summary, folded.String())
	}
}

func (t table) render(b *strings.Builder) {
	if len(t.rows) == 0 {
		return
	}
	if t.heading != "" {
		fmt.Fprintf(b, "**%s**\n\n", t.heading)
	}
	if t.note != "" {
		fmt.Fprintf(b, "%s\n\n", t.note)
	}
	// The grouping column is dropped when every row shares a group, which is the
	// case for a size table: the binary is already named in the heading.
	header := []string{t.item, "base", "head", "Δ", ""}
	align := []string{"---", "---:", "---:", "---:", "---:"}
	if t.group != "" {
		header = append([]string{t.group}, header...)
		align = append([]string{"---"}, align...)
	}
	writeRow(b, header)
	writeRow(b, align)
	for _, r := range t.rows {
		cells := []string{
			escapePipes(r.Name),
			t.side(r.Base, r.baseOK), t.side(r.Head, r.headOK),
			signedBy(r.Delta(), t.format), percent(r),
		}
		if t.group != "" {
			cells = append([]string{escapePipes(r.Group)}, cells...)
		}
		writeRow(b, cells)
	}
	if t.omitted > 0 {
		fmt.Fprintf(b, "\n_%d smaller change(s) not shown._\n", t.omitted)
	}
	b.WriteString("\n")
}

// side renders one measurement, distinguishing a quantity of zero from a
// measurement that was never taken because the item is absent on that side.
func (t table) side(v float64, ok bool) string {
	if !ok {
		return "—"
	}
	return t.format(v)
}

func writeRow(b *strings.Builder, cells []string) {
	b.WriteString("|")
	for _, c := range cells {
		b.WriteString(" ")
		b.WriteString(c)
		b.WriteString(" |")
	}
	b.WriteString("\n")
}

// escapePipes keeps a name containing a pipe, which Go symbol and type names
// can, from breaking out of its table cell.
func escapePipes(s string) string { return strings.ReplaceAll(s, "|", `\|`) }

// trimPkg drops the module prefix from a package path so the column shows the
// part that varies.
func trimPkg(pkg, module string) string {
	if pkg == "" {
		return "—"
	}
	if module == "" {
		return pkg
	}
	if pkg == module {
		return "."
	}
	return strings.TrimPrefix(pkg, module+"/")
}
