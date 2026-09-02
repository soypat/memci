package main

import (
	"strings"
	"testing"
)

func TestToolchains(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     config
		want    []string
		targets []string // Expected -targets per toolchain, in the same order.
	}{
		{name: "go only", cfg: config{targets: "./cmd/..."},
			want: []string{"go"}, targets: []string{"./cmd/..."}},
		{name: "both", cfg: config{targets: "./cmd/...", tinygo: "tinygo"},
			want: []string{"go", "tinygo"}, targets: []string{"./cmd/...", "./cmd/..."}},
		// A repo whose host build is uninteresting, or whose firmware is the
		// only thing worth sizing, gets just the one toolchain.
		{name: "tinygo only", cfg: config{tinygo: "tinygo", tinygoTargets: "./firmware"},
			want: []string{"tinygo"}, targets: []string{"./firmware"}},
		{name: "narrowed", cfg: config{targets: "./...", tinygo: "tinygo", tinygoTargets: "./firmware"},
			want: []string{"go", "tinygo"}, targets: []string{"./...", "./firmware"}},
		// -tinygo without any pattern to build has nothing to do; it must not
		// produce a toolchain that later fails with "no main packages".
		{name: "no targets at all", cfg: config{tinygo: "tinygo"}, want: nil},
		{name: "nothing", cfg: config{}, want: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := toolchains(tc.cfg)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d toolchains, want %v", len(got), tc.want)
			}
			for i, w := range tc.want {
				if got[i].name != w {
					t.Errorf("toolchain %d is %q, want %q", i, got[i].name, w)
				}
				if got[i].targets != tc.targets[i] {
					t.Errorf("%s builds %q, want %q", w, got[i].targets, tc.targets[i])
				}
			}
		})
	}
}

func TestToolchainMeasurementBasis(t *testing.T) {
	tcs := toolchains(config{targets: ".", tinygo: "tinygo"})
	// go binaries are compared as files: that is what gets shipped, and .bss is
	// a virtual reservation the process never pays for. tinygo binaries are
	// compared as loadable images: .bss is real RAM, and the file carries DWARF
	// that moves with the checkout path because tinygo has no -trimpath.
	if tcs[0].mem {
		t.Error("go sizes are measured as a loadable image, not as file bytes")
	}
	if !tcs[1].mem {
		t.Error("tinygo sizes are measured as file bytes, which are not reproducible across checkout paths")
	}
	cfg := config{bindiff: "bindiff", kind: "package"}
	if got := strings.Join(bindiffArgv(cfg, tcs[0], "a", "b"), " "); strings.Contains(got, "-mem") {
		t.Errorf("go invocation asks for -mem: %s", got)
	}
	if got := strings.Join(bindiffArgv(cfg, tcs[1], "a", "b"), " "); !strings.Contains(got, "-mem") {
		t.Errorf("tinygo invocation does not ask for -mem: %s", got)
	}
}

func TestBuildArgv(t *testing.T) {
	tcs := toolchains(config{targets: ".", tinygo: "tinygo", tinygoFlags: "-target=pico -opt=z"})

	want := "go build -trimpath -buildvcs=false -o /tmp/out ./cmd/blinky"
	if got := strings.Join(buildArgv(tcs[0], "./cmd/blinky", "/tmp/out"), " "); got != want {
		t.Errorf("go build argv = %q, want %q", got, want)
	}
	// tinygo rejects -trimpath and -buildvcs, so the go flags must not leak
	// across; its own flags carry the target, which is the whole point of them.
	want = "tinygo build -target=pico -opt=z -o /tmp/out ./cmd/blinky"
	if got := strings.Join(buildArgv(tcs[1], "./cmd/blinky", "/tmp/out"), " "); got != want {
		t.Errorf("tinygo build argv = %q, want %q", got, want)
	}
	// A command may carry arguments of its own, the way -bindiff does, so that a
	// repo can point at a tinygo it builds itself.
	custom := toolchains(config{targets: ".", tinygo: "go run ./cmd/tinygo"})
	want = "go run ./cmd/tinygo build -o /tmp/out ."
	if got := strings.Join(buildArgv(custom[1], ".", "/tmp/out"), " "); got != want {
		t.Errorf("custom tinygo argv = %q, want %q", got, want)
	}
}

// totals is the set of rows measureSize hands to sideBySide: one per binary and
// toolchain, carrying whole-binary sizes.
func totals(rows ...Row) []Row { return rows }

func totalRow(binary, tool string, base, head float64) Row {
	return Row{Group: binary, Name: tool, Unit: "bytes",
		Base: base, Head: head, baseOK: base != 0, headOK: head != 0}
}

func TestSideBySideKeepsToolchainsAdjacent(t *testing.T) {
	tcs := toolchains(config{targets: ".", tinygo: "tinygo"})
	// blinky's go build moved by far the most, and sensor's tinygo build by the
	// least. Ordering by magnitude alone would interleave the two binaries.
	rows := totals(
		totalRow("blinky", "go", 1_400_000, 1_700_000),
		totalRow("blinky", "tinygo", 17_000, 18_000),
		totalRow("sensor", "go", 1_100_000, 1_150_000),
		totalRow("sensor", "tinygo", 12_000, 11_900),
	)
	tab, ok := sideBySide(rows, tcs)
	if !ok {
		t.Fatal("no table produced for four changed binaries")
	}
	var got []string
	for _, r := range tab.rows {
		got = append(got, r.Group+"/"+r.Name)
	}
	want := []string{"blinky/go", "blinky/tinygo", "sensor/go", "sensor/tinygo"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("row order = %v, want %v", got, want)
	}
	if tab.group != "binary" || tab.item != "toolchain" {
		t.Errorf("table columns are %q/%q, want binary/toolchain", tab.group, tab.item)
	}
}

func TestSideBySideKeepsTheUnchangedHalf(t *testing.T) {
	tcs := toolchains(config{targets: ".", tinygo: "tinygo"})
	// The comparison this table exists for is "the change cost the host build
	// 300 KiB and the firmware nothing", so the unchanged tinygo row stays even
	// though every other table in the report would drop it.
	rows := totals(
		totalRow("blinky", "go", 1_400_000, 1_700_000),
		totalRow("blinky", "tinygo", 17_000, 17_000),
	)
	tab, ok := sideBySide(rows, tcs)
	if !ok {
		t.Fatal("no table produced")
	}
	if len(tab.rows) != 2 {
		t.Fatalf("got %d rows, want the changed and the unchanged toolchain", len(tab.rows))
	}
	if tab.rows[1].Name != "tinygo" || tab.rows[1].Delta() != 0 {
		t.Errorf("the unchanged toolchain was dropped: %+v", tab.rows)
	}
}

func TestSideBySideDropsBinariesThatDidNotMove(t *testing.T) {
	tcs := toolchains(config{targets: ".", tinygo: "tinygo"})
	rows := totals(
		totalRow("blinky", "go", 1_400_000, 1_700_000),
		totalRow("blinky", "tinygo", 17_000, 17_000),
		totalRow("sensor", "go", 1_100_000, 1_100_000),
		totalRow("sensor", "tinygo", 12_000, 12_000),
	)
	tab, _ := sideBySide(rows, tcs)
	for _, r := range tab.rows {
		if r.Group == "sensor" {
			t.Errorf("a binary that moved under no toolchain was listed: %+v", r)
		}
	}
	// And when nothing moved at all there is no table, so the section is just
	// its headline.
	if _, ok := sideBySide(rows[2:], tcs); ok {
		t.Error("a table was produced for binaries that did not change")
	}
}

func TestBasisNote(t *testing.T) {
	both := toolchains(config{targets: ".", tinygo: "tinygo"})
	note := basisNote(both)
	if !strings.Contains(note, "`go` rows are the bytes of the file") ||
		!strings.Contains(note, "`tinygo` rows are the loadable image") {
		t.Errorf("the note does not say what each toolchain counts: %s", note)
	}
	// With one toolchain there is nothing to confuse, so the note would be
	// noise.
	if got := basisNote(toolchains(config{targets: "."})); got != "" {
		t.Errorf("a single toolchain got a basis note: %s", got)
	}
	if got := basisNote(toolchains(config{tinygo: "tinygo", tinygoTargets: "."})); got != "" {
		t.Errorf("a single toolchain got a basis note: %s", got)
	}
}

func TestBinaryLabel(t *testing.T) {
	one := toolchains(config{targets: "."})
	two := toolchains(config{targets: ".", tinygo: "tinygo"})
	if got := binaryLabel("blinky", one[0], one); got != "blinky" {
		t.Errorf("label = %q, want an unqualified name when only one toolchain built it", got)
	}
	if got := binaryLabel("blinky", two[1], two); got != "blinky (tinygo)" {
		t.Errorf("label = %q, want the toolchain named", got)
	}
}

func TestQuantity(t *testing.T) {
	if got := quantity(1, "binary", "binaries"); got != "1 binary" {
		t.Errorf("quantity(1) = %q", got)
	}
	if got := quantity(2, "binary", "binaries"); got != "2 binaries" {
		t.Errorf("quantity(2) = %q", got)
	}
}
