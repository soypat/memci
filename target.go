package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// target is one binary to measure: a command that builds it, and where that
// command leaves the ELF. The same command runs in both checkouts.
type target struct {
	Name  string `json:"name"`  // Label in the report. Defaults to the ELF's base name.
	Build string `json:"build"` // Shell command, run from the root of each checkout.
	ELF   string `json:"elf"`   // Where Build leaves the binary, relative to the checkout unless absolute.
	// Mem measures the loadable image plus .bss rather than the file's bytes.
	// Firmware ships an image into scarce RAM; a host binary ships a file whose
	// .bss is a virtual reservation nobody pays for.
	Mem bool `json:"mem"`
}

// parseTargets decodes the -targets-json value. An action input can only ever
// be a string, so a workflow passes this as a block scalar.
func parseTargets(raw string) ([]target, error) {
	var out []target
	dec := json.NewDecoder(strings.NewReader(raw))
	// A typo in a field name would otherwise be ignored and the target measured
	// on the wrong basis.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("parsing -targets-json: %w", err)
	}
	if len(out) == 0 {
		return nil, errors.New("-targets-json is an empty list")
	}
	for i := range out {
		if out[i].Build == "" {
			return nil, fmt.Errorf("target %d has no build command", i)
		}
		if out[i].ELF == "" {
			return nil, fmt.Errorf("target %q has no elf path", out[i].Build)
		}
		if out[i].Name == "" {
			out[i].Name = defaultName(out[i].ELF)
		}
	}
	if err := uniqueNames(out); err != nil {
		return nil, err
	}
	return out, nil
}

// defaultName is the ELF's base name without its extension, so that
// "build/fw.elf" is reported as "fw".
func defaultName(elf string) string {
	base := path.Base(filepath.ToSlash(elf))
	return strings.TrimSuffix(base, path.Ext(base))
}

// uniqueNames rejects a list where two targets would be reported under one
// name. Two builds writing the same ELF is fine and expected -- they run one
// after the other -- but they need telling apart in the report.
func uniqueNames(targets []target) error {
	seen := make(map[string]bool, len(targets))
	for _, t := range targets {
		if seen[t.Name] {
			return fmt.Errorf("two targets are both named %q; give at least one an explicit name", t.Name)
		}
		seen[t.Name] = true
	}
	return nil
}

// sugarTargets expands the -targets package patterns into one host build per
// main package, so the common case need not be spelled out in JSON. Only
// packages on both revisions are built; a hand written build gets no such
// treatment, and one that fails on the base revision fails the run.
func sugarTargets(cfg config, outDir string) ([]target, error) {
	pkgs, err := mainPackages(cfg)
	if err != nil {
		return nil, err
	}
	if len(pkgs) == 0 {
		return nil, fmt.Errorf("no main packages matched %q", cfg.targets)
	}
	out := make([]target, 0, len(pkgs))
	for _, pkg := range pkgs {
		name := path.Base(pkg)
		elf := filepath.Join(outDir, name) // Keeps artifacts out of the checkout.
		out = append(out, target{
			Name: name,
			// -trimpath and -buildvcs=false drop the two things that differ
			// between checkouts for reasons unrelated to the change: the build
			// directory and the stamped commit.
			Build: fmt.Sprintf("go build -trimpath -buildvcs=false -o %s %s",
				shellQuote(elf), shellQuote(pkg)),
			ELF: elf,
		})
	}
	return out, uniqueNames(out)
}

// shellQuote makes an argument safe for the shell the build command runs in, so
// a temp directory with a space in it does not split into two arguments.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// elfPath resolves where a target's binary landed in one checkout. The result
// is absolute because it is handed to bindiff, which runs in a different
// directory than memci does.
func (t target) elfPath(dir string) string {
	p := t.ELF
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

// build runs a target's build command in one checkout. It goes through a shell
// so it can be written the way it would be typed.
func build(cfg config, t target, dir string) error {
	_, err := command(cfg, dir, []string{"sh", "-c", t.Build})
	return err
}

// stash copies a freshly built binary aside. Both sides run the same build
// command, so the head build can otherwise land on top of the base one.
func stash(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("the build did not produce %s: %w", src, err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// bindiffArgv assembles the bindiff invocation. cfg.bindiff may carry arguments
// of its own so that a repo can point at an uninstalled copy with
// "go run ./cmd/bindiff".
func bindiffArgv(cfg config, t target, oldPath, newPath string) []string {
	argv := strings.Fields(cfg.bindiff)
	argv = append(argv, "-json", "-kind="+cfg.kind)
	if t.Mem {
		argv = append(argv, "-mem")
	}
	return append(argv, "diff", oldPath, newPath)
}

// totalsTable puts every target's bottom line in one place. It earns its space
// only when there are several: with one target the headline already carries the
// number.
func totalsTable(rows []Row, targets []target) (table, bool) {
	if len(targets) < 2 {
		return table{}, false
	}
	rows = keep(rows, tolerance{})
	if len(rows) == 0 {
		return table{}, false
	}
	sortRows(rows)
	return table{
		heading: "Totals", note: basisNote(targets),
		item:   "target",
		format: humanBytes, rows: rows,
	}, true
}

// basisNote says what the totals are counting when the targets do not all count
// the same thing. Putting a file's bytes next to a loadable image is fine as
// long as the table admits that is what it is doing.
func basisNote(targets []target) string {
	var file, mem []string
	for _, t := range targets {
		if t.Mem {
			mem = append(mem, "`"+t.Name+"`")
		} else {
			file = append(file, "`"+t.Name+"`")
		}
	}
	if len(file) == 0 || len(mem) == 0 {
		return ""
	}
	return fmt.Sprintf("_%s counts the bytes of the file; %s counts the loadable image plus `.bss`, which is what has to fit on the device._",
		strings.Join(file, " and "), strings.Join(mem, " and "))
}

// commandNote is the build line shown under a target's detail table. Reporting
// the command itself rather than a reconstruction of its flags means tags,
// -opt, -ldflags and everything else are covered without plumbing each one
// through, and it says exactly what was measured.
func commandNote(t target) string {
	return "`" + strings.Join(strings.Fields(t.Build), " ") + "`"
}
