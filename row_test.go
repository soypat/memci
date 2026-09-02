package main

import "testing"

func TestToleranceDropsUnchanged(t *testing.T) {
	// The report is a list of changes. A row whose measurement did not move is
	// the single most common case and must never reach a table.
	r := Row{Base: 100, Head: 100, baseOK: true, headOK: true}
	if (tolerance{}).significant(r) {
		t.Error("an unchanged row is significant")
	}
}

func TestToleranceBounds(t *testing.T) {
	row := func(base, head float64) Row {
		return Row{Base: base, Head: head, baseOK: true, headOK: true}
	}
	tol := tolerance{abs: 8, pct: 1}
	for _, tc := range []struct {
		name string
		row  Row
		want bool
	}{
		{"below both bounds", row(1000, 1004), false},
		{"above bytes, below percent", row(10000, 10020), false},
		{"above percent, below bytes", row(100, 104), false},
		{"above both bounds", row(1000, 1100), true},
		{"shrinkage counts too", row(1000, 900), true},
		// The bounds say "smaller than", so a change that lands exactly on them
		// is reported.
		{"exactly on both bounds", row(800, 808), true},
		{"exactly on bytes, under percent", row(1600, 1608), false},
	} {
		if got := tol.significant(tc.row); got != tc.want {
			t.Errorf("%s: significant = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestToleranceAlwaysKeepsAppearances(t *testing.T) {
	// A benchmark or symbol that appeared or vanished is structural news, not
	// noise, however small the quantity involved.
	tol := tolerance{abs: 1e9, pct: 1e9}
	added := Row{Head: 1, headOK: true}
	removed := Row{Base: 1, baseOK: true}
	if !tol.significant(added) || !tol.significant(removed) {
		t.Error("an appearance or disappearance was filtered out as noise")
	}
	// A row absent from both sides is not news; it is nothing.
	if tol.significant(Row{}) {
		t.Error("an empty row is significant")
	}
}

func TestZeroToleranceKeepsEveryChange(t *testing.T) {
	// Allocation counts and binary sizes are exact, so the smallest possible
	// change must survive.
	r := Row{Base: 1000, Head: 1001, baseOK: true, headOK: true}
	if !(tolerance{}).significant(r) {
		t.Error("a one unit change was dropped at zero tolerance")
	}
}

func TestJoinMarksOneSidedRows(t *testing.T) {
	base := map[key]float64{{"p", "Both"}: 10, {"p", "OnlyBase"}: 5}
	head := map[key]float64{{"p", "Both"}: 12, {"p", "OnlyHead"}: 7}
	rows := join("B/op", base, head)
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	byName := map[string]Row{}
	for _, r := range rows {
		byName[r.Name] = r
	}
	if r := byName["OnlyHead"]; !r.Added() || r.Removed() {
		t.Errorf("OnlyHead: added=%v removed=%v, want added", r.Added(), r.Removed())
	}
	if r := byName["OnlyBase"]; !r.Removed() || r.Added() {
		t.Errorf("OnlyBase: added=%v removed=%v, want removed", r.Added(), r.Removed())
	}
	if r := byName["Both"]; r.Added() || r.Removed() || r.Delta() != 2 {
		t.Errorf("Both: %+v, want a plain +2 change", r)
	}
}

func TestJoinDistinguishesZeroFromAbsent(t *testing.T) {
	// A benchmark that legitimately reports 0 allocs/op must not be confused
	// with one that does not exist on that side; they render differently.
	rows := join("allocs/op",
		map[key]float64{{"p", "B"}: 0},
		map[key]float64{{"p", "B"}: 3},
	)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].Added() {
		t.Error("a measured zero was reported as an addition")
	}
	if !rows[0].baseOK {
		t.Error("a measured zero was recorded as unmeasured")
	}
}

func TestSortRowsByMagnitude(t *testing.T) {
	rows := []Row{
		{Name: "small", Base: 100, Head: 101, baseOK: true, headOK: true},
		{Name: "shrunk", Base: 100, Head: 50, baseOK: true, headOK: true},
		{Name: "grown", Base: 100, Head: 130, baseOK: true, headOK: true},
	}
	sortRows(rows)
	want := []string{"shrunk", "grown", "small"}
	for i, name := range want {
		if rows[i].Name != name {
			t.Fatalf("position %d is %q, want %q (a large shrink must outrank a small growth)",
				i, rows[i].Name, name)
		}
	}
}

func TestSortRowsIsStableByName(t *testing.T) {
	rows := []Row{
		{Group: "b", Name: "x", Base: 1, Head: 2, baseOK: true, headOK: true},
		{Group: "a", Name: "y", Base: 1, Head: 2, baseOK: true, headOK: true},
	}
	sortRows(rows)
	if rows[0].Group != "a" {
		t.Error("equal deltas are not ordered by group, so the report is not reproducible")
	}
}

func TestTrim(t *testing.T) {
	rows := make([]Row, 10)
	shown, omitted := trim(rows, 4)
	if len(shown) != 4 || omitted != 6 {
		t.Errorf("trim(10, 4) = %d shown, %d omitted; want 4, 6", len(shown), omitted)
	}
	if shown, omitted := trim(rows, 0); len(shown) != 10 || omitted != 0 {
		t.Errorf("trim(10, 0) = %d shown, %d omitted; want all rows", len(shown), omitted)
	}
	if shown, omitted := trim(rows, 20); len(shown) != 10 || omitted != 0 {
		t.Errorf("trim(10, 20) = %d shown, %d omitted; want all rows", len(shown), omitted)
	}
}

func TestPercent(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  Row
		want string
	}{
		{"growth", Row{Base: 100, Head: 150, baseOK: true, headOK: true}, "+50.0%"},
		{"shrinkage", Row{Base: 100, Head: 50, baseOK: true, headOK: true}, "-50.0%"},
		{"added", Row{Head: 10, headOK: true}, "new"},
		{"removed", Row{Base: 10, baseOK: true}, "gone"},
		{"from a measured zero", Row{Base: 0, Head: 10, baseOK: true, headOK: true}, ""},
	} {
		if got := percent(tc.row); got != tc.want {
			t.Errorf("%s: percent = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestHumanBytes(t *testing.T) {
	for _, tc := range []struct {
		in   float64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1 KiB"},
		{1536, "1.50 KiB"},
		{1 << 20, "1 MiB"},
		{-2048, "-2 KiB"},
	} {
		if got := humanBytes(tc.in); got != tc.want {
			t.Errorf("humanBytes(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSignedBy(t *testing.T) {
	if got := signedBy(1024, humanBytes); got != "+1 KiB" {
		t.Errorf("growth = %q, want +1 KiB", got)
	}
	if got := signedBy(-1024, humanBytes); got != "-1 KiB" {
		t.Errorf("shrinkage = %q, want -1 KiB", got)
	}
}
