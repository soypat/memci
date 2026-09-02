package main

import "testing"

// bindiffDiffJSON is a trimmed copy of `bindiff -json -kind=package diff` output.
// It pins the field names memci depends on, so a change to bindiff's schema
// fails here rather than silently producing an empty report in CI.
const bindiffDiffJSON = `{
  "mode": "diff",
  "kind": "package",
  "old": "base-httpsrv",
  "new": "head-httpsrv",
  "entries": [
    {"kind": "package", "name": "encoding/json", "old": 0, "new": 59092},
    {"kind": "package", "name": "runtime", "old": 501482, "new": 502301},
    {"kind": "package", "name": "old/pkg", "old": 4096, "new": 0},
    {"kind": "package", "name": "[unattributed]", "section": ".text", "old": 100, "new": 220}
  ],
  "summary": {"old": 505678, "new": 561613, "delta": 55935}
}`

func TestSizeRows(t *testing.T) {
	rows, delta, err := sizeRows("httpsrv", []byte(bindiffDiffJSON))
	if err != nil {
		t.Fatal(err)
	}
	if delta != 55935 {
		t.Errorf("delta = %d, want the summary's 55935", delta)
	}
	if len(rows) != 4 {
		t.Fatalf("got %d rows, want 4", len(rows))
	}
	byName := map[string]Row{}
	for _, r := range rows {
		if r.Group != "httpsrv" {
			t.Errorf("row %q attributed to %q, want httpsrv", r.Name, r.Group)
		}
		if r.Unit != "bytes" {
			t.Errorf("row %q carries unit %q, want bytes", r.Name, r.Unit)
		}
		byName[r.Name] = r
	}
	if r := byName["encoding/json"]; !r.Added() {
		t.Error("a package linked in only by head is not reported as new")
	}
	if r := byName["old/pkg"]; !r.Removed() {
		t.Error("a package dropped by head is not reported as gone")
	}
	// A remainder row exists once per section, so it has to carry the section
	// name or two of them collide in the table.
	if _, ok := byName["[unattributed] .text"]; !ok {
		t.Errorf("the unattributed row lost its section; got names %v", names(rows))
	}
}

func TestSizeRowsRejectsAProfile(t *testing.T) {
	// A profile has no base side. Rendering one as a diff would show every
	// package in the binary as newly added, so it is refused outright.
	_, _, err := sizeRows("httpsrv", []byte(`{"mode":"profile","entries":[],"summary":{}}`))
	if err == nil {
		t.Fatal("a profile report was accepted as a diff")
	}
}

func TestSizeRowsRejectsGarbage(t *testing.T) {
	if _, _, err := sizeRows("httpsrv", []byte("not json")); err == nil {
		t.Fatal("malformed bindiff output was accepted")
	}
}

func names(rows []Row) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Name
	}
	return out
}
