// Package result is the shared verdict/result vocabulary for Transit-WPT: the
// graded outcome of running one implementation through one scenario, as judged
// by the oracle layer, and the matrix those results render into. Kept dependency
// -free so the oracle, report, and orchestrator all agree on the contract.
package result

import "sort"

// Verdict is a graded conformance outcome. Ordered by severity so a run's
// rollup is simply the worst of its checks.
type Verdict int

const (
	Pass        Verdict = iota // behaved correctly
	Warn                       // suspicious but permitted (e.g. a legal-but-odd choice)
	Fail                       // a conformance invariant was violated
	Unsupported                // the implementation does not support this scenario
	Error                      // the run itself failed (setup/crash) — not a conformance result
)

func (v Verdict) String() string {
	switch v {
	case Pass:
		return "PASS"
	case Warn:
		return "WARN"
	case Fail:
		return "FAIL"
	case Unsupported:
		return "UNSUPPORTED"
	case Error:
		return "ERROR"
	default:
		return "?"
	}
}

// Check is one oracle assertion's outcome.
type Check struct {
	Name    string  `json:"name"`
	Verdict Verdict `json:"verdict"`
	Detail  string  `json:"detail,omitempty"` // the violating event / explanation
}

// Result is the graded outcome of one (implementation × scenario) run.
type Result struct {
	Lib      string             `json:"lib"`
	Scenario string             `json:"scenario"`
	Checks   []Check            `json:"checks"`
	Metrics  map[string]float64 `json:"metrics,omitempty"` // goodput, deliveryPct, p50/p99, retransmits...
	Err      string             `json:"error,omitempty"`
}

// Verdict rolls up the run to its worst check (or Error if the run failed).
func (r Result) Verdict() Verdict {
	if r.Err != "" {
		return Error
	}
	worst := Pass
	for _, c := range r.Checks {
		if c.Verdict > worst {
			worst = c.Verdict
		}
	}
	return worst
}

// Matrix is the full set of results plus the axes for rendering.
type Matrix struct {
	Title     string   `json:"title"`
	Generated string   `json:"generated"` // RFC3339, stamped by the caller (no clock here)
	Libs      []string `json:"libs"`
	Scenarios []string `json:"scenarios"`
	Results   []Result `json:"results"`
}

// Get returns the result for a (lib, scenario) cell, or false if absent.
func (m Matrix) Get(lib, scenario string) (Result, bool) {
	for _, r := range m.Results {
		if r.Lib == lib && r.Scenario == scenario {
			return r, true
		}
	}
	return Result{}, false
}

// NewMatrix builds a Matrix, deriving sorted Libs/Scenarios axes from results.
func NewMatrix(title, generated string, results []Result) Matrix {
	libSet := map[string]bool{}
	scnSet := map[string]bool{}
	for _, r := range results {
		libSet[r.Lib] = true
		scnSet[r.Scenario] = true
	}
	return Matrix{
		Title:     title,
		Generated: generated,
		Libs:      sortedKeys(libSet),
		Scenarios: sortedKeys(scnSet),
		Results:   results,
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
