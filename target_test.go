package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseTargets(t *testing.T) {
	got, err := parseTargets(`[
		{"build": "go build -o host.elf ./cmd/x", "elf": "host.elf"},
		{"name": "fw", "build": "tinygo build -o build/fw.elf -target=pico ./cmd/x", "elf": "build/fw.elf", "mem": true}
	]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d targets, want 2", len(got))
	}
	// A name is optional; the ELF's base name without its extension is enough
	// when each target writes its own file.
	if got[0].Name != "host" {
		t.Errorf("default name = %q, want host", got[0].Name)
	}
	if got[0].Mem {
		t.Error("mem defaulted to true; a host binary is measured as a file")
	}
	if got[1].Name != "fw" || !got[1].Mem {
		t.Errorf("explicit fields lost: %+v", got[1])
	}
}

func TestParseTargetsRejectsIncomplete(t *testing.T) {
	for _, tc := range []struct {
		name, json, want string
	}{
		{"no build", `[{"elf": "a.elf"}]`, "build command"},
		{"no elf", `[{"build": "go build ."}]`, "elf path"},
		{"not json", `go build .`, "parsing"},
		// A typo in a field name would otherwise be silently ignored and the
		// target measured with the wrong basis.
		{"unknown field", `[{"build": "go build .", "elf": "a.elf", "memory": true}]`, "memory"},
	} {
		_, err := parseTargets(tc.json)
		if err == nil {
			t.Errorf("%s: accepted", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q does not mention %q", tc.name, err, tc.want)
		}
	}
}

func TestParseTargetsRejectsDuplicateNames(t *testing.T) {
	// Two builds writing the same ELF is fine -- they run one after the other --
	// but they would report under one name, so the list has to say which is which.
	_, err := parseTargets(`[
		{"build": "go build -o bin.elf ./cmd/x", "elf": "bin.elf"},
		{"build": "tinygo build -o bin.elf ./cmd/x", "elf": "bin.elf"}
	]`)
	if err == nil {
		t.Fatal("two targets named bin were accepted")
	}
	if !strings.Contains(err.Error(), "explicit name") {
		t.Errorf("error %q does not say how to fix it", err)
	}
}

func TestLoadBuilds(t *testing.T) {
	const list = `[{"build": "go build -o a.elf .", "elf": "a.elf"}]`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "memci.json"), []byte(list), 0o600); err != nil {
		t.Fatal(err)
	}

	// A path is resolved against the head checkout, so the build list travels
	// with the code rather than living only in the workflow.
	got, err := loadBuilds(config{headDir: dir, targetsJSON: "memci.json"})
	if err != nil {
		t.Fatal(err)
	}
	if got != list {
		t.Errorf("file contents = %q, want %q", got, list)
	}

	// An inline document is used as it stands, even though a build command in
	// it may well end in .json.
	inline := `[{"build": "go build -o x .", "elf": "x", "name": "x.json"}]`
	got, err = loadBuilds(config{headDir: dir, targetsJSON: inline})
	if err != nil {
		t.Fatal(err)
	}
	if got != inline {
		t.Errorf("inline JSON was not passed through: %q", got)
	}
}

func TestLoadBuildsMissingFile(t *testing.T) {
	_, err := loadBuilds(config{headDir: t.TempDir(), targetsJSON: "nope.json"})
	if err == nil {
		t.Fatal("a missing build list was accepted")
	}
	if !strings.Contains(err.Error(), "build list") {
		t.Errorf("error %q does not say what could not be read", err)
	}
}

func TestElfPath(t *testing.T) {
	rel := target{ELF: "build/fw.elf"}
	if got := rel.elfPath("/checkout"); got != "/checkout/build/fw.elf" {
		t.Errorf("relative elf resolved to %q", got)
	}
	// An absolute path is the same file for both checkouts, which is why the
	// base build is stashed before the head build runs.
	abs := target{ELF: "/tmp/out.elf"}
	if got := abs.elfPath("/checkout"); got != "/tmp/out.elf" {
		t.Errorf("absolute elf resolved to %q", got)
	}
	// bindiff runs in the checkout, not where memci was invoked, so a path
	// relative to memci's own directory would not resolve for it.
	if got := rel.elfPath("testdata"); !filepath.IsAbs(got) {
		t.Errorf("elfPath returned the relative %q", got)
	}
}

func TestBindiffArgv(t *testing.T) {
	cfg := config{bindiff: "go run ./cmd/bindiff", kind: "package"}
	got := strings.Join(bindiffArgv(cfg, target{}, "old", "new"), " ")
	want := "go run ./cmd/bindiff -json -kind=package diff old new"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	// mem is the difference between measuring what ships as a file and what has
	// to fit on a device.
	got = strings.Join(bindiffArgv(cfg, target{Mem: true}, "old", "new"), " ")
	if !strings.Contains(got, " -mem ") {
		t.Errorf("a mem target did not ask bindiff for the image: %q", got)
	}
}

func TestCommandNote(t *testing.T) {
	// The report quotes the command rather than reconstructing its flags, so
	// tags, -opt and -ldflags all come along without plumbing each one.
	got := commandNote(target{Build: "tinygo build  -o fw.elf   -target=pico -panic=trap ./cmd/fw"})
	want := "`tinygo build -o fw.elf -target=pico -panic=trap ./cmd/fw`"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestTotalsTable(t *testing.T) {
	rows := []Row{
		{Name: "host", Unit: "bytes", Base: 100, Head: 150, baseOK: true, headOK: true},
		{Name: "fw", Unit: "bytes", Base: 100, Head: 90, baseOK: true, headOK: true},
	}
	targets := []target{{Name: "host"}, {Name: "fw", Mem: true}}

	// One target's total is already in the headline, so the table would only
	// repeat it.
	if _, ok := totalsTable(rows[:1], targets[:1]); ok {
		t.Error("a single target produced a totals table")
	}

	tbl, ok := totalsTable(rows, targets)
	if !ok {
		t.Fatal("two targets produced no totals table")
	}
	if tbl.rows[0].Name != "host" {
		t.Errorf("rows not ordered by magnitude: %+v", tbl.rows)
	}
	if !strings.Contains(tbl.note, "`fw`") || !strings.Contains(tbl.note, "loadable image") {
		t.Errorf("the note does not say the two rows count different things: %q", tbl.note)
	}
}

func TestTotalsTableDropsUnchangedTargets(t *testing.T) {
	rows := []Row{
		{Name: "host", Unit: "bytes", Base: 100, Head: 100, baseOK: true, headOK: true},
		{Name: "fw", Unit: "bytes", Base: 100, Head: 100, baseOK: true, headOK: true},
	}
	targets := []target{{Name: "host"}, {Name: "fw"}}
	if _, ok := totalsTable(rows, targets); ok {
		t.Error("targets that did not move produced a totals table")
	}
}

func TestBasisNote(t *testing.T) {
	// With every target measured the same way there is nothing to disclaim.
	if got := basisNote([]target{{Name: "a"}, {Name: "b"}}); got != "" {
		t.Errorf("uniform targets produced a note: %q", got)
	}
	if got := basisNote([]target{{Name: "a", Mem: true}, {Name: "b", Mem: true}}); got != "" {
		t.Errorf("uniform mem targets produced a note: %q", got)
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("/tmp/a b/x"); got != `'/tmp/a b/x'` {
		t.Errorf("got %s", got)
	}
	if got := shellQuote("it's"); got != `'it'\''s'` {
		t.Errorf("got %s", got)
	}
}

func TestQuantity(t *testing.T) {
	// A firmware repo often has one command and one benchmark, and "1 binaries"
	// in a PR comment reads like a bug.
	if got := quantity(1, "binary", "binaries"); got != "1 binary" {
		t.Errorf("got %q", got)
	}
	if got := quantity(3, "binary", "binaries"); got != "3 binaries" {
		t.Errorf("got %q", got)
	}
}
