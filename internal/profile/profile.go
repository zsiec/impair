// Package profile is a G.1050/TIA-921-INSPIRED library of realistic
// network-impairment profiles for the Impair engine. It maps a small set of
// named, graded "service levels" — each describing a coherent end-to-end IP
// path quality — onto concrete scenario.Stage parameters (loss, delay, delay
// variation/jitter, and optional reordering), then turns a Profile plus a seed
// into a runnable scenario.Scenario.
//
// # Provenance and disclaimer
//
// ITU-T Rec. G.1050 and its TIA counterpart TIA-921 ("Network model for
// evaluating multimedia transmission performance over Internet Protocol")
// define a path of five concatenated segments, eight graded network "service
// level behaviours" (from lightly loaded to heavily congested), and a large
// catalogue (~133) of link-speed configurations. The realized impairments the
// model emphasizes are packet delay, delay variation, packet loss, and
// out-of-sequence (reordered) packets.
//
// The full specification text is paywalled. This package is therefore a
// G.1050-INSPIRED *parameterization*: it reproduces the STRUCTURE — a graded
// ladder of named service levels spanning excellent to severe IP impairment,
// each carrying loss / base-delay / delay-variation / reorder knobs — but the
// concrete numbers below are engineering approximations chosen to span a
// representative range, NOT values transcribed from the standard. Do not treat
// this as a verbatim or conformant implementation of G.1050/TIA-921.
//
// References (structure only):
//   - https://www.itu.int/rec/T-REC-G.1050
//   - https://www.soft-switch.org/spandsp-doc/g1050_ip_network_model_page.html
package profile

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/zsiec/impair/scenario"
)

// LossModel selects how a profile's loss is realized when compiled to a Stage.
type LossModel string

const (
	// LossBernoulli emits an independent per-packet loss stage (scenario.LossParams).
	LossBernoulli LossModel = "bernoulli"
	// LossGE emits a Gilbert-Elliott burst-loss stage (scenario.GEParams),
	// closer to real congested-path loss which clusters.
	LossGE LossModel = "ge"
)

// Profile is a named, seedable network service level. It is the G.1050-inspired
// description of one end-to-end IP path quality, in friendly units (percentages
// and milliseconds). Compile turns it into a scenario.Scenario.
type Profile struct {
	// Name is the stable identifier (e.g. "g1050-A").
	Name string `json:"name"`
	// Description is a one-line human summary of the service level.
	Description string `json:"description"`
	// Grade is the ordinal position on the impairment ladder (0 = pristine,
	// higher = worse). Used only for ordering/reporting.
	Grade int `json:"grade"`

	// LossModel selects Bernoulli vs Gilbert-Elliott loss realization.
	LossModel LossModel `json:"lossModel,omitempty"`
	// LossPct is the mean packet-loss percentage (0..100). For LossGE it is the
	// target steady-state loss the (P,R) pair is derived to approximate.
	LossPct float64 `json:"lossPct"`
	// BurstR is the Gilbert-Elliott "bad->good" recovery probability (only used
	// when LossModel==LossGE). Smaller R => longer loss bursts. Ignored for
	// Bernoulli.
	BurstR float64 `json:"burstR,omitempty"`

	// BaseDelayMs is the one-way propagation/serialization base delay.
	BaseDelayMs float64 `json:"baseDelayMs"`
	// JitterMs is the peak delay variation (uniform +/- around base). Maps to
	// scenario DelayParams.JitterMs.
	JitterMs float64 `json:"jitterMs,omitempty"`
	// DelayDist is the jitter distribution ("uniform" or "normal"). Empty =>
	// uniform. When "normal", SigmaMs is used instead of JitterMs.
	DelayDist string `json:"delayDist,omitempty"`
	// SigmaMs is the normal-distribution standard deviation (only for
	// DelayDist=="normal").
	SigmaMs float64 `json:"sigmaMs,omitempty"`
	// DelayCorrelation is the per-packet delay correlation (0..1), modeling
	// slowly-varying queue depth.
	DelayCorrelation float64 `json:"delayCorrelation,omitempty"`

	// ReorderPct is the percentage of packets reordered (0 => no reorder stage).
	ReorderPct float64 `json:"reorderPct,omitempty"`
	// ReorderGapMs is how far reordered packets are delayed relative to the
	// stream (the "gap" they jump behind).
	ReorderGapMs float64 `json:"reorderGapMs,omitempty"`
}

// geParams derives Gilbert-Elliott (P, R) from a target mean loss and a chosen
// recovery probability R. With H=1 (lossless good) and K=0 (total loss in bad),
// the GE steady-state loss is P/(P+R), so to hit target loss L (fraction) for a
// given R we solve P = L*R/(1-L). This keeps the configured BurstR as the burst
// length knob while LossPct stays the loss target.
func geParams(lossPct, r float64) (p, rr float64) {
	l := lossPct / 100
	if l <= 0 {
		return 0, r
	}
	if l >= 1 {
		l = 0.999
	}
	if r <= 0 {
		r = 0.5
	}
	p = l * r / (1 - l)
	return p, r
}

// Compile turns the Profile into a scenario.Scenario with the given seed. The
// pipeline follows the engine's canonical chain: loss -> reorder -> delay,
// applied symmetrically to both directions. Stages with zero magnitude are
// omitted so a pristine profile still yields at least the base-delay stage.
func (p Profile) Compile(seed int64) scenario.Scenario {
	var stages []scenario.Stage

	// 1. Loss stage (if any).
	if p.LossPct > 0 {
		switch p.LossModel {
		case LossGE:
			gp, gr := geParams(p.LossPct, p.BurstR)
			stages = append(stages, scenario.Stage{GE: &scenario.GEParams{P: gp, R: gr}})
		default: // LossBernoulli or unset
			stages = append(stages, scenario.Stage{Loss: &scenario.LossParams{P: p.LossPct / 100}})
		}
	}

	// 2. Reorder stage (if any).
	if p.ReorderPct > 0 {
		stages = append(stages, scenario.Stage{Reorder: &scenario.ReorderParams{
			ReorderPct: p.ReorderPct / 100,
			GapMs:      p.ReorderGapMs,
		}})
	}

	// 3. Delay stage. Always present (base delay is non-negative; even a
	// pristine path has a tiny base) so every profile produces a non-empty
	// pipeline.
	dp := &scenario.DelayParams{
		BaseMs:      p.BaseDelayMs,
		Correlation: p.DelayCorrelation,
	}
	switch p.DelayDist {
	case "normal":
		dp.Distribution = "normal"
		dp.SigmaMs = p.SigmaMs
	default:
		if p.JitterMs > 0 {
			dp.Distribution = "uniform"
			dp.JitterMs = p.JitterMs
		}
	}
	stages = append(stages, scenario.Stage{Delay: dp})

	return scenario.Scenario{
		Name:     p.Name,
		Seed:     seed,
		Pipeline: stages,
	}
}

// Profiles returns the built-in G.1050-inspired service-level ladder, keyed by
// name. The set spans a graded range from pristine to severe IP impairment.
//
// Service-level mapping (G.1050-INSPIRED approximation — see package doc):
//
//	g1050-A  pristine    backbone / managed LAN: ~0% loss, low delay, no jitter
//	g1050-B  good        well-provisioned broadband: tiny loss, modest delay+jitter
//	g1050-C  lte         mobile/LTE-ish: moderate burst loss, higher delay+jitter, light reorder
//	g1050-D  congested   heavily congested access: appreciable burst loss, large jittery delay, reorder
//	g1050-E  severe      stressed/lossy edge: severe burst loss, very large delay variation, heavy reorder
func Profiles() map[string]Profile {
	return map[string]Profile{
		"g1050-A": {
			Name:        "g1050-A",
			Description: "pristine backbone / managed LAN (excellent)",
			Grade:       0,
			LossModel:   LossBernoulli,
			LossPct:     0,
			BaseDelayMs: 1,
		},
		"g1050-B": {
			Name:        "g1050-B",
			Description: "well-provisioned broadband (good)",
			Grade:       1,
			LossModel:   LossBernoulli,
			LossPct:     0.1,
			BaseDelayMs: 20,
			JitterMs:    3,
			DelayDist:   "uniform",
		},
		"g1050-C": {
			Name:             "g1050-C",
			Description:      "mobile / LTE-ish access (fair)",
			Grade:            2,
			LossModel:        LossGE,
			LossPct:          1.0,
			BurstR:           0.5,
			BaseDelayMs:      45,
			SigmaMs:          12,
			DelayDist:        "normal",
			DelayCorrelation: 0.3,
			ReorderPct:       1.0,
			ReorderGapMs:     5,
		},
		"g1050-D": {
			Name:             "g1050-D",
			Description:      "heavily congested access (poor)",
			Grade:            3,
			LossModel:        LossGE,
			LossPct:          3.0,
			BurstR:           0.35,
			BaseDelayMs:      90,
			SigmaMs:          30,
			DelayDist:        "normal",
			DelayCorrelation: 0.4,
			ReorderPct:       3.0,
			ReorderGapMs:     10,
		},
		"g1050-E": {
			Name:             "g1050-E",
			Description:      "stressed / lossy edge (severe)",
			Grade:            4,
			LossModel:        LossGE,
			LossPct:          8.0,
			BurstR:           0.25,
			BaseDelayMs:      150,
			SigmaMs:          60,
			DelayDist:        "normal",
			DelayCorrelation: 0.5,
			ReorderPct:       6.0,
			ReorderGapMs:     20,
		},
	}
}

// Names returns the built-in profile names sorted by impairment grade (then
// name), i.e. pristine -> severe.
func Names() []string {
	ps := Profiles()
	names := make([]string, 0, len(ps))
	for n := range ps {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		a, b := ps[names[i]], ps[names[j]]
		if a.Grade != b.Grade {
			return a.Grade < b.Grade
		}
		return names[i] < names[j]
	})
	return names
}

// Get returns the named built-in profile and whether it exists.
func Get(name string) (Profile, bool) {
	p, ok := Profiles()[name]
	return p, ok
}

// Scenario is the convenience helper: it looks up the named built-in profile and
// compiles it with seed. It errors if the name is unknown.
func Scenario(name string, seed int64) (scenario.Scenario, error) {
	p, ok := Get(name)
	if !ok {
		return scenario.Scenario{}, fmt.Errorf("profile: unknown profile %q", name)
	}
	return p.Compile(seed), nil
}

// Load parses a Profile from JSON (the persistence path for custom profiles).
func Load(r io.Reader) (Profile, error) {
	var p Profile
	err := json.NewDecoder(r).Decode(&p)
	return p, err
}

// Save writes a Profile as indented JSON.
func Save(w io.Writer, p Profile) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(p)
}
