package main

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Row is one line of a comparison table: a named quantity measured on the base
// revision and on the head revision.
//
// Both halves of memci reduce to this. A benchmark's B/op is a named quantity;
// so is the number of bytes a Go package contributes to a binary. Sharing the
// row means there is one place that decides what counts as a change, one
// ordering, and one renderer, rather than one of each per metric.
type Row struct {
	Group string  // Benchmark package, or binary name for sizes.
	Name  string  // Benchmark name, or entry name for sizes.
	Unit  string  // "B/op", "allocs/op" or "bytes".
	Base  float64 // Quantity on the base revision. Zero means absent.
	Head  float64 // Quantity on the head revision. Zero means absent.

	// baseOK and headOK distinguish "measured as zero" from "not measured at
	// all", so a benchmark that only exists on one side reads as added or
	// removed rather than as a change to or from zero.
	baseOK, headOK bool
}

func (r Row) Delta() float64 { return r.Head - r.Base }

// Added reports whether the row exists only on the head revision, and Removed
// only on the base revision.
func (r Row) Added() bool   { return !r.baseOK && r.headOK }
func (r Row) Removed() bool { return r.baseOK && !r.headOK }

// tolerance describes how large a change must be before it is worth a row in
// the report. Both bounds must be exceeded, so a small absolute change on a
// large quantity and a large relative change on a tiny one are both suppressed.
//
// The zero tolerance reports every non-zero change, which is what the exactly
// reproducible metrics (allocs/op, binary bytes) want.
type tolerance struct {
	abs float64 // Minimum absolute delta.
	pct float64 // Minimum delta as a percentage of the base.
}

// significant reports whether a row's change is large enough to show.
// Appearances and disappearances always are: they are structural, not noise.
func (t tolerance) significant(r Row) bool {
	if r.Added() || r.Removed() {
		return r.Base != 0 || r.Head != 0
	}
	d := math.Abs(r.Delta())
	if d == 0 {
		return false
	}
	if d < t.abs {
		return false
	}
	if t.pct > 0 && r.Base != 0 && 100*d/math.Abs(r.Base) < t.pct {
		return false
	}
	return true
}

// keep filters rows down to the significant changes. This is the whole of the
// "only show what moved" requirement; every table in the report goes through it.
func keep(rows []Row, t tolerance) []Row {
	out := rows[:0:0]
	for _, r := range rows {
		if t.significant(r) {
			out = append(out, r)
		}
	}
	return out
}

// sortRows orders by magnitude of change, largest first, so the rows that
// explain a regression come first. Ties break by name to keep the report
// stable across runs.
func sortRows(rows []Row) {
	sort.SliceStable(rows, func(i, j int) bool {
		di, dj := math.Abs(rows[i].Delta()), math.Abs(rows[j].Delta())
		if di != dj {
			return di > dj
		}
		if rows[i].Group != rows[j].Group {
			return rows[i].Group < rows[j].Group
		}
		return rows[i].Name < rows[j].Name
	})
}

// join pairs measurements taken on each side into rows. Keys absent from one
// side yield an added or removed row.
func join(unit string, base, head map[key]float64) []Row {
	rows := make([]Row, 0, len(head))
	seen := make(map[key]bool, len(head))
	for k, h := range head {
		seen[k] = true
		b, ok := base[k]
		rows = append(rows, Row{
			Group: k.group, Name: k.name, Unit: unit,
			Base: b, Head: h, baseOK: ok, headOK: true,
		})
	}
	for k, b := range base {
		if seen[k] {
			continue
		}
		rows = append(rows, Row{
			Group: k.group, Name: k.name, Unit: unit,
			Base: b, baseOK: true,
		})
	}
	return rows
}

// key identifies the same measurement across the two revisions.
type key struct{ group, name string }

// signedBy renders a delta through the column's own formatter, with an explicit
// sign so growth and shrinkage are distinguishable at a glance. Formatters
// already carry the minus sign for negatives.
func signedBy(v float64, format func(float64) string) string {
	s := format(v)
	if v > 0 {
		return "+" + s
	}
	return s
}

// trim keeps the n rows with the largest changes and reports how many were
// dropped. Rows must already be sorted by magnitude. A PR comment is capped at
// 65536 characters, and a table nobody will read to the end is not more
// informative for being complete.
func trim(rows []Row, n int) ([]Row, int) {
	if n <= 0 || len(rows) <= n {
		return rows, 0
	}
	return rows[:n], len(rows) - n
}

// percent renders the relative change. An item that appeared or vanished has no
// meaningful percentage.
func percent(r Row) string {
	switch {
	case r.Added():
		return "new"
	case r.Removed():
		return "gone"
	case r.Base == 0:
		return ""
	}
	return fmt.Sprintf("%+.1f%%", 100*r.Delta()/r.Base)
}

// trimFloat formats a value without a trailing ".0"; most of these quantities
// are whole numbers that happen to be carried as floats.
func trimFloat(v float64) string {
	s := strconv.FormatFloat(v, 'f', 2, 64)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}
