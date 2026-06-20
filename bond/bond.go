// Package bond is the SMPTE 2022-7 seamless-redundancy model and oracle — the
// multi-link payoff of Impair's per-link seeded substreams (scenario.BuildLink).
//
// A bonded sender emits the SAME media over N independent paths; a 2022-7
// receiver merges by sequence number, so a packet survives as long as it
// survives on AT LEAST ONE path. This package computes that merge from per-link
// drop ledgers (Merge / RunLinks) and grades the outcome (Evaluate): the marquee
// invariant is that a burst on one link is MASKED — delivered with no gap —
// because an independent redundant link carried it. The only true losses are
// packets dropped on EVERY link at once, which no amount of spatial redundancy
// can recover and which are therefore not a bonding fault.
package bond

import (
	"fmt"

	"github.com/zsiec/impair/engine"
	"github.com/zsiec/impair/result"
)

// LinkLedger is one bonded path's realized fate over a run: the set of ingress
// sequence numbers the link DROPPED (everything else it forwarded). Exact sets
// come from an instrumented (Tier-1) drive via RunLinks; a black-box (Tier-2)
// path that only knows a count leaves Dropped nil and sets DropCount.
type LinkLedger struct {
	Dropped   map[uint64]bool
	DropCount int
}

// Drops returns the number of packets this link dropped.
func (l LinkLedger) Drops() int {
	if l.Dropped != nil {
		return len(l.Dropped)
	}
	return l.DropCount
}

// MergeResult is the outcome of merging per-link drop ledgers under 2022-7.
type MergeResult struct {
	Total  int      // ingress packets (seqs 1..Total)
	Links  int      // number of bonded paths
	Gaps   []uint64 // seqs dropped on EVERY link (unrecoverable even with redundancy)
	Masked int      // seqs dropped on >=1 link but carried by another (the payoff)
}

// Delivered is the count a perfect 2022-7 merge yields: every packet that
// survived on at least one link.
func (m MergeResult) Delivered() int { return m.Total - len(m.Gaps) }

// Merge computes the 2022-7 seamless-redundancy outcome over exact per-link drop
// sets for a stream of `total` ingress packets (seqs 1..total). It requires
// every ledger's Dropped set to be populated (the Tier-1 exact path); the
// Tier-2 measured path grades from Evaluate's Delivered/Gaps fields instead.
func Merge(links []LinkLedger, total int) MergeResult {
	res := MergeResult{Total: total, Links: len(links)}
	if len(links) == 0 {
		return res
	}
	for seq := uint64(1); seq <= uint64(total); seq++ {
		dropped := 0
		for i := range links {
			if links[i].Dropped[seq] {
				dropped++
			}
		}
		switch {
		case dropped == len(links): // lost on ALL links -> a real gap
			res.Gaps = append(res.Gaps, seq)
		case dropped > 0: // lost on some, carried by another -> masked
			res.Masked++
		}
	}
	return res
}

// RunLinks drives the SAME deterministic ingress stream — `total` C2S packets
// spaced `interval` ns — through each link's engine and returns one exact
// LinkLedger per link. It is Sans-I/O and bit-deterministic: the substrate for a
// PROVABLE 2022-7 masking proof. The engines must be independent
// (scenario.BuildLink) for the masking to be meaningful; feeding identical
// packets in identical order makes engine i's Nth ingress Seq == N on every
// link, so a ledger's seqs line up across links for Merge.
func RunLinks(engines []*engine.Engine, total int, interval int64) []LinkLedger {
	ledgers := make([]LinkLedger, len(engines))
	for i := range ledgers {
		ledgers[i].Dropped = make(map[uint64]bool)
	}
	for n := 0; n < total; n++ {
		at := int64(n) * interval
		data := []byte{byte(n), byte(n >> 8)} // deterministic, content-irrelevant
		for i, eng := range engines {
			for _, a := range eng.Handle(engine.Packet{Data: data, Dir: engine.C2S}, at) {
				if a.Kind == engine.Drop {
					ledgers[i].Dropped[a.Seq] = true
				}
			}
		}
	}
	return ledgers
}

// Input is what the bonding oracle grades: the bonded run accounting plus the
// per-link drop ground truth. Delivered is the distinct-sequence count the
// merged receiver actually delivered (measured for Tier-2; MergeResult.Delivered
// for Tier-1). Gaps is the exact count of seqs lost on ALL links when known
// (Tier-1 via Merge, or 0 when a link was provably clean), or -1 when unknown
// (a black-box run that only has per-link drop counts).
type Input struct {
	Lib, Scenario string
	Links         int
	Total         int
	Delivered     int
	PerLinkDrops  []int
	Gaps          int // exact all-link-loss count, or -1 if unknown
	Metrics       map[string]float64
}

func (in Input) totalDrops() int {
	s := 0
	for _, d := range in.PerLinkDrops {
		s += d
	}
	return s
}

// Evaluate grades a bonded (2022-7) run into result.Checks, in a stable order.
func Evaluate(in Input) []result.Check {
	return []result.Check{
		bondedFlow(in),
		seamlessCoverage(in),
		redundancyActive(in),
	}
}

// ResultFor wraps Evaluate into a result.Result for the given run.
func ResultFor(in Input) result.Result {
	return result.Result{Lib: in.Lib, Scenario: in.Scenario, Checks: Evaluate(in), Metrics: in.Metrics}
}

// 1. bonded-flow: a bonded stream needs >= 2 paths and must have carried media.
func bondedFlow(in Input) result.Check {
	c := result.Check{Name: "bonded-flow"}
	switch {
	case in.Links < 2:
		c.Verdict = result.Fail
		c.Detail = fmt.Sprintf("bonding needs >= 2 paths, got %d", in.Links)
	case in.Total > 0 && in.Delivered > 0:
		c.Verdict = result.Pass
		c.Detail = fmt.Sprintf("%d paths; delivered %d/%d", in.Links, in.Delivered, in.Total)
	case in.Total > 0:
		c.Verdict = result.Fail
		c.Detail = fmt.Sprintf("%d packets offered but none delivered over %d paths", in.Total, in.Links)
	default:
		c.Verdict = result.Error
		c.Detail = "no packets offered — run produced no evidence"
	}
	return c
}

// 2. seamless-coverage: THE 2022-7 payoff. Every packet that survived on at least
// one link MUST reach the merged output — a single-link burst must be masked.
// With exact ledgers, expected = Total - all-link-losses; Delivered below that
// means the receiver dropped a packet a redundant link carried (a real bonding
// FAIL). all-link-losses themselves are unrecoverable, not a fault.
func seamlessCoverage(in Input) result.Check {
	c := result.Check{Name: "seamless-coverage"}
	td := in.totalDrops()
	if in.Gaps >= 0 { // exact: all-link-loss count known. The shortfall FAIL gate
		// is checked FIRST so a merge that loses a survivable packet can never be
		// masked — even when no per-link drop was recorded (td == 0).
		expected := in.Total - in.Gaps
		switch {
		case in.Delivered < expected:
			c.Verdict = result.Fail
			c.Detail = fmt.Sprintf("seamless merge FAILED: delivered %d/%d but %d survived on >= 1 link — dropped %d packet(s) a redundant path carried",
				in.Delivered, in.Total, expected, expected-in.Delivered)
		case td == 0:
			c.Verdict = result.Pass
			c.Detail = fmt.Sprintf("no per-link loss; clean merge delivered %d/%d", in.Delivered, in.Total)
		case in.Gaps == 0:
			c.Verdict = result.Pass
			c.Detail = fmt.Sprintf("seamless: %d per-link drop(s) across %v ALL masked by redundancy, 0 gap (delivered %d/%d)",
				td, in.PerLinkDrops, in.Delivered, in.Total)
		default:
			c.Verdict = result.Pass
			c.Detail = fmt.Sprintf("seamless merge intact: delivered %d/%d; %d seq(s) lost on ALL %d links (unrecoverable, not a bonding fault)",
				in.Delivered, in.Total, in.Gaps, in.Links)
		}
		return c
	}
	// Gaps unknown (black-box): judge from delivery alone — can't separate an
	// all-link loss from a genuine merge failure, so a shortfall is only a WARN.
	switch {
	case in.Delivered >= in.Total && td == 0:
		c.Verdict = result.Pass
		c.Detail = fmt.Sprintf("no per-link loss recorded; delivered %d/%d", in.Delivered, in.Total)
	case in.Delivered >= in.Total:
		c.Verdict = result.Pass
		c.Detail = fmt.Sprintf("delivered %d/%d despite per-link drops %v — redundancy masked all single-link loss",
			in.Delivered, in.Total, in.PerLinkDrops)
	default:
		c.Verdict = result.Warn
		c.Detail = fmt.Sprintf("delivered %d/%d (%d short) under per-link drops %v — gap vs merge-failure indistinguishable without exact ledgers",
			in.Delivered, in.Total, in.Total-in.Delivered, in.PerLinkDrops)
	}
	return c
}

// 3. redundancy-active: did the run actually exercise redundancy (some path lost
// packets the others had to mask)? Informational — the teeth are in (2).
func redundancyActive(in Input) result.Check {
	c := result.Check{Name: "redundancy-active"}
	c.Verdict = result.Pass
	if td := in.totalDrops(); td > 0 {
		c.Detail = fmt.Sprintf("redundancy exercised: per-link drops %v (%d single-link losses available to mask)", in.PerLinkDrops, td)
	} else {
		c.Detail = "no per-link loss this run (clean paths — redundancy not exercised)"
	}
	return c
}
