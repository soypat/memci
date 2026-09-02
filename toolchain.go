package main

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// toolchain is one compiler the targets are built with. A change that is free
// in a host binary can be expensive on a microcontroller and the other way
// around, so memci measures each configured toolchain separately instead of
// assuming one number stands for both.
type toolchain struct {
	name    string   // "go" or "tinygo". Names the rows and the table headings.
	argv    []string // Command to run, e.g. ["tinygo"].
	flags   []string // Build flags every build gets.
	targets string   // Comma separated package patterns to build.

	// mem asks bindiff for the loadable image plus .bss instead of the file's
	// bytes. Which one is the honest measure depends on the toolchain, so it
	// travels with it rather than being a flag of its own.
	//
	// For a host binary the file is what gets shipped and .bss is a virtual
	// reservation nobody pays for: the Go runtime alone reserves tens of
	// megabytes of .noptrbss that is never touched, which would swamp the
	// totals and dilute every percentage in the table. For firmware both facts
	// invert -- .bss is scarce RAM and the image is what gets flashed.
	mem bool
}

// toolchains returns the toolchains that have something to build, in report
// order. Either half can be empty: a firmware repo may size only the TinyGo
// build, and most repos only the host one.
func toolchains(cfg config) []toolchain {
	var out []toolchain
	if cfg.targets != "" {
		out = append(out, toolchain{
			name: "go",
			argv: []string{"go"},
			// -trimpath and -buildvcs=false remove the two things that would
			// otherwise differ between the checkouts for reasons that have
			// nothing to do with the change: the directory the build ran in,
			// and the stamped commit.
			flags:   []string{"-trimpath", "-buildvcs=false"},
			targets: cfg.targets,
		})
	}
	if cfg.tinygo != "" {
		targets := cfg.tinygoTargets
		if targets == "" {
			targets = cfg.targets
		}
		if targets != "" {
			out = append(out, toolchain{
				name:    "tinygo",
				argv:    strings.Fields(cfg.tinygo),
				flags:   strings.Fields(cfg.tinygoFlags),
				targets: targets,
				// tinygo has no -trimpath, so the checkout path reaches the
				// DWARF and two builds of identical source differ by the length
				// of the directory they were built in. None of those bytes are
				// loadable, so the image -mem measures is reproducible where the
				// file is not.
				mem: true,
			})
		}
	}
	return out
}

// build compiles one package with one toolchain.
func build(cfg config, tc toolchain, dir, target, out string) error {
	_, err := command(cfg, dir, buildArgv(tc, target, out))
	return err
}

// buildArgv assembles a build command. Both toolchains spell it the same way,
// but only the flags of the one being run are passed: tinygo has neither
// -trimpath nor -buildvcs and would reject them.
func buildArgv(tc toolchain, target, out string) []string {
	argv := append([]string{}, tc.argv...)
	argv = append(argv, "build")
	argv = append(argv, tc.flags...)
	return append(argv, "-o", out, target)
}

// bindiffArgv assembles the bindiff invocation. cfg.bindiff may carry arguments
// of its own so that a repo can point at an uninstalled copy with
// "go run ./cmd/bindiff".
func bindiffArgv(cfg config, tc toolchain, oldPath, newPath string) []string {
	argv := strings.Fields(cfg.bindiff)
	argv = append(argv, "-json", "-kind="+cfg.kind)
	if tc.mem {
		argv = append(argv, "-mem")
	}
	return append(argv, "diff", oldPath, newPath)
}

// binaryLabel names a binary in a table heading, qualified by toolchain only
// when more than one built it.
func binaryLabel(name string, tc toolchain, tcs []toolchain) string {
	if len(tcs) < 2 {
		return name
	}
	return fmt.Sprintf("%s (%s)", name, tc.name)
}

// sideBySide builds the table that puts each binary's toolchains next to each
// other. A binary is listed under every toolchain as soon as one of them moved:
// "go grew and tinygo did not" is exactly the comparison the table exists to
// make, so dropping the unchanged half would defeat it.
func sideBySide(rows []Row, tcs []toolchain) (table, bool) {
	moved := make(map[string]bool)
	byBinary := make(map[string][]Row)
	largest := make(map[string]float64)
	var names []string
	for _, r := range rows {
		if _, ok := byBinary[r.Group]; !ok {
			names = append(names, r.Group)
		}
		byBinary[r.Group] = append(byBinary[r.Group], r)
		if d := math.Abs(r.Delta()); d > largest[r.Group] {
			largest[r.Group] = d
		}
		if (tolerance{}).significant(r) {
			moved[r.Group] = true
		}
	}
	// Largest change first, matching every other table in the report.
	sort.SliceStable(names, func(i, j int) bool {
		if largest[names[i]] != largest[names[j]] {
			return largest[names[i]] > largest[names[j]]
		}
		return names[i] < names[j]
	})

	var out []Row
	for _, name := range names {
		if !moved[name] {
			continue
		}
		// Toolchain order rather than magnitude order, so that a binary's rows
		// stay adjacent and read the same way for every binary in the table.
		for _, tc := range tcs {
			for _, r := range byBinary[name] {
				if r.Name == tc.name {
					out = append(out, r)
				}
			}
		}
	}
	if len(out) == 0 {
		return table{}, false
	}
	return table{
		heading: "Totals", note: basisNote(tcs),
		group: "binary", item: "toolchain",
		format: humanBytes, rows: out,
	}, true
}

// basisNote says what the totals are counting when the toolchains do not count
// the same thing. Putting a file's bytes next to a loadable image is fine as
// long as the table admits that is what it is doing.
func basisNote(tcs []toolchain) string {
	var file, mem []string
	for _, tc := range tcs {
		if tc.mem {
			mem = append(mem, "`"+tc.name+"`")
		} else {
			file = append(file, "`"+tc.name+"`")
		}
	}
	if len(file) == 0 || len(mem) == 0 {
		return ""
	}
	return fmt.Sprintf("_%s rows are the bytes of the file; %s rows are the loadable image plus `.bss`, which is what has to fit on the device._",
		strings.Join(file, " and "), strings.Join(mem, " and "))
}
