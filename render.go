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

// section holds the tables for one half of the report plus a headline that is
// worth stating even when nothing changed underneath it.
type section struct {
	heading  string
	headline string
	tables   []table
}

// render writes the whole Markdown report. marker is an HTML comment that lets
// a PR commenter find and update its previous comment rather than posting a new
// one each push.
func render(w io.Writer, marker, title, subtitle string, sections []section) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n### %s\n\n", marker, title)
	if subtitle != "" {
		fmt.Fprintf(&b, "%s\n\n", subtitle)
	}

	any := false
	for _, s := range sections {
		rendered := s.render(&b)
		any = any || rendered
	}
	if !any {
		b.WriteString("No change in binary size or benchmark allocations.\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// render writes a section and reports whether it contributed anything. A
// section with a headline is worth printing even with no tables under it: "the
// binary did not change size" is a result.
func (s section) render(b *strings.Builder) bool {
	var body strings.Builder
	for _, t := range s.tables {
		t.render(&body)
	}
	if body.Len() == 0 && s.headline == "" {
		return false
	}
	fmt.Fprintf(b, "#### %s\n\n", s.heading)
	if s.headline != "" {
		fmt.Fprintf(b, "%s\n\n", s.headline)
	}
	b.WriteString(body.String())
	return true
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
		b.WriteString(" " + c + " |")
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
