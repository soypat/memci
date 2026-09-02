package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// bindiffReport is the subset of bindiff's `-json diff` output that memci
// consumes. It is deliberately a narrow copy rather than a shared import:
// bindiff's model lives in a package main, and pinning memci to a specific
// version of it would make the action harder to use, not easier.
type bindiffReport struct {
	Mode    string         `json:"mode"`
	Kind    string         `json:"kind"`
	Entries []bindiffEntry `json:"entries"`
	Summary bindiffSummary `json:"summary"`
}

// bindiffSummary is the whole-binary total. Delta is what a size budget is
// applied to; Old and New are what the side by side table shows, since "grew by
// 1 KiB" means something different on a 4 MiB host binary than on a 20 KiB
// firmware image.
type bindiffSummary struct {
	Old   int64 `json:"old"`
	New   int64 `json:"new"`
	Delta int64 `json:"delta"`
}

type bindiffEntry struct {
	Name    string `json:"name"`
	Section string `json:"section"`
	Old     int64  `json:"old"`
	New     int64  `json:"new"`
}

// unattributed is bindiff's name for the bytes of a section that no entry of
// the requested granularity accounts for. It occurs once per section, so it
// needs the section name to stay distinguishable.
const unattributed = "[unattributed]"

func (e bindiffEntry) displayName() string {
	if e.Name == unattributed && e.Section != "" {
		return e.Name + " " + e.Section
	}
	return e.Name
}

// sizeRows turns one bindiff diff report into rows attributed to target.
//
// bindiff already drops entries whose size did not move, so in practice every
// entry here is a change; the rows still go through keep so that both halves of
// the report obey exactly one rule about what is worth showing.
func sizeRows(target string, raw []byte) ([]Row, bindiffSummary, error) {
	var rep bindiffReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		return nil, bindiffSummary{}, fmt.Errorf("decoding bindiff output for %s: %w", target, err)
	}
	if rep.Mode != "diff" {
		return nil, bindiffSummary{}, fmt.Errorf("bindiff report for %s has mode %q, want diff", target, rep.Mode)
	}
	rows := make([]Row, 0, len(rep.Entries))
	for _, e := range rep.Entries {
		rows = append(rows, Row{
			Group: target,
			Name:  e.displayName(),
			Unit:  "bytes",
			Base:  float64(e.Old),
			Head:  float64(e.New),
			// An entry bindiff reports at all was measured on both sides; a
			// zero side means the item is absent there, which is exactly what
			// baseOK and headOK are for.
			baseOK: e.Old != 0,
			headOK: e.New != 0,
		})
	}
	return rows, rep.Summary, nil
}

// humanBytes renders a byte count at a readable scale. Size tables mix
// single-byte deltas with kilobyte ones and a raw column of digits is hard to
// scan; the exact number stays in the base and head columns.
func humanBytes(v float64) string {
	neg := v < 0
	if neg {
		v = -v
	}
	var s string
	switch {
	case v >= 1<<20:
		s = fmt.Sprintf("%.2f MiB", v/(1<<20))
	case v >= 1<<10:
		s = fmt.Sprintf("%.2f KiB", v/(1<<10))
	default:
		s = fmt.Sprintf("%s B", trimFloat(v))
	}
	s = strings.Replace(s, ".00 ", " ", 1)
	if neg {
		return "-" + s
	}
	return s
}
