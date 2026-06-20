// Package fec is the SMPTE ST 2022-1 forward-error-correction recoverability
// model and oracle — the FEC twin of the bond package. Where bonding recovers a
// loss because a redundant LINK carried it, FEC recovers a loss because the
// matrix's column (and, in 2-D, row) parity packet can reconstruct it. The model
// (Matrix, Recover) computes, from the realized droplist alone, EXACTLY which
// losses an ST 2022-1 receiver can recover and which are residual; the oracle
// (Evaluate) cross-checks a real run's measured delivery and self-reported
// recovery count against that ground truth — never trusting the implementation's
// own FEC stat, only checking it cannot exceed what the matrix physically allows.
//
// Matrix geometry (ST 2022-1 / TR-06-3): media packets are laid out row-major in
// an L-column by D-row block; packet i sits at row i/L, column i%L. A column FEC
// packet XORs the D packets in its column (which are spaced L apart, so a burst
// of consecutive losses lands in DISTINCT columns — the interleave that makes FEC
// burst-resilient); a row FEC packet (2-D only) XORs the L packets in its row.
// Single-erasure decoding per line, iterated to a fixpoint, recovers a single
// loss per column (1-D) or per row OR column with cascade (2-D).
//
// Scope: v0 models MEDIA loss with the FEC packets assumed intact — exactly the
// Transit-WPT setup where the relay impairs the media stream and forwards the
// column/row FEC clean. A lost FEC packet (which forfeits its line's recovery) is
// a documented v0 omission, not a silent one.
package fec

import (
	"fmt"
	"sort"

	"github.com/zsiec/impair/result"
)

// Matrix is an ST 2022-1 FEC geometry: an L-column by D-row interleaved block.
type Matrix struct {
	L, D int  // columns, rows (TR-06-3 2022-1: L,D in [1,20]/[4,20], L*D <= 100)
	TwoD bool // row FEC present (2-D) vs column-only (1-D)
}

// Block is the number of media packets one FEC matrix protects (L*D).
func (m Matrix) Block() int { return m.L * m.D }

func (m Matrix) col(i int) int { return i % m.L }
func (m Matrix) row(i int) int { return i / m.L }

// Valid reports whether the geometry is usable (positive dimensions).
func (m Matrix) Valid() bool { return m.L > 0 && m.D > 0 }

// Recover applies the ST 2022-1 erasure decode to the lost media indices within
// ONE block (each in [0, L*D)) and returns which losses FEC recovers and which
// remain (residual), both sorted ascending. A packet is recoverable when it is
// the sole outstanding loss in its column (1-D) or in its row or column (2-D),
// iterated to a fixpoint since recovering one loss can expose the next. The
// recovered/residual sets are a pure function of (matrix, lost) — independent of
// decode order — so this is a deterministic ground truth. Out-of-range and
// duplicate indices are ignored.
func Recover(m Matrix, lost []int) (recovered, residual []int) {
	out := make(map[int]bool)
	for _, i := range lost {
		if i >= 0 && i < m.Block() {
			out[i] = true
		}
	}
	colCount := make(map[int]int)
	rowCount := make(map[int]int)
	for i := range out {
		colCount[m.col(i)]++
		rowCount[m.row(i)]++
	}
	for {
		// Smallest still-lost index that is alone in its column (or, in 2-D, its
		// row). Smallest-first only fixes the decode ORDER; the final sets are
		// order-independent.
		next := -1
		for i := range out {
			if colCount[m.col(i)] == 1 || (m.TwoD && rowCount[m.row(i)] == 1) {
				if next == -1 || i < next {
					next = i
				}
			}
		}
		if next == -1 {
			break
		}
		delete(out, next)
		colCount[m.col(next)]--
		rowCount[m.row(next)]--
		recovered = append(recovered, next)
	}
	for i := range out {
		residual = append(residual, i)
	}
	sort.Ints(recovered)
	sort.Ints(residual)
	return
}

// Input is what the FEC oracle grades. Lost is the ground-truth set of media
// indices the engine dropped (the realized droplist) within the protected block;
// the oracle computes the genuinely-recoverable set from Matrix and cross-checks
// it against measured Delivered and the implementation's self-reported ClaimedFEC
// (evidence to cross-examine — checked for plausibility, never trusted as truth).
type Input struct {
	Lib, Scenario string
	Matrix        Matrix
	Total         int   // FEC-protected media packets sent
	Lost          []int // ground-truth lost media indices (the realized droplist)
	Delivered     int   // distinct media packets delivered at the receiver (measured)
	ClaimedFEC    int   // impl's self-reported FEC-recovered count, or -1 if unknown
	// ARQIsolated is true when retransmission was suppressed (e.g. the relay drops
	// RTCP), so recovery is FEC-ONLY and delivery above the recoverable ceiling is
	// physically impossible — which lets the soundness check bite on delivery, not
	// just on the self-reported count.
	ARQIsolated bool
	Metrics     map[string]float64
}

// Evaluate grades a FEC run into result.Checks, in a stable order.
func Evaluate(in Input) []result.Check {
	recovered, residual := Recover(in.Matrix, in.Lost)
	return []result.Check{
		fecProtected(in),
		fecRecoverySound(in, len(recovered), len(residual)),
		fecRecoveryEffective(in, len(recovered), len(residual)),
		fecActive(in, len(recovered)),
	}
}

// ResultFor wraps Evaluate into a result.Result for the given run.
func ResultFor(in Input) result.Result {
	return result.Result{Lib: in.Lib, Scenario: in.Scenario, Checks: Evaluate(in), Metrics: in.Metrics}
}

// 1. fec-protected: a FEC matrix must be configured over a media stream.
func fecProtected(in Input) result.Check {
	c := result.Check{Name: "fec-protected"}
	switch {
	case !in.Matrix.Valid():
		c.Verdict = result.Fail
		c.Detail = fmt.Sprintf("invalid FEC matrix L=%d D=%d", in.Matrix.L, in.Matrix.D)
	case in.Total > 0:
		c.Verdict = result.Pass
		c.Detail = fmt.Sprintf("%dx%d %s FEC over %d media packet(s)", in.Matrix.L, in.Matrix.D, dim(in.Matrix.TwoD), in.Total)
	default:
		c.Verdict = result.Error
		c.Detail = "no media — run produced no evidence"
	}
	return c
}

// 2. fec-recovery-sound: the implementation must not recover MORE than the matrix
// physically allows. Two independent witnesses, either of which bites:
//   - the self-reported count cannot exceed the recoverable set, and
//   - with ARQ isolated, delivery cannot exceed (sent - residual).
//
// A violation is a hard FAIL: a fabricated/over-counted FEC stat, or a recovery
// FEC alone cannot explain (ARQ leaked into a supposedly FEC-only run, or a
// non-conformant decode).
func fecRecoverySound(in Input, recoverable, residual int) result.Check {
	c := result.Check{Name: "fec-recovery-sound"}
	if in.ClaimedFEC >= 0 && in.ClaimedFEC > recoverable {
		c.Verdict = result.Fail
		c.Detail = fmt.Sprintf("implementation claims %d FEC recoveries but the matrix allows at most %d for this droplist (over-reported)",
			in.ClaimedFEC, recoverable)
		return c
	}
	ceiling := in.Total - residual // the most a FEC-only receiver can deliver
	if in.ARQIsolated && in.Delivered > ceiling {
		c.Verdict = result.Fail
		c.Detail = fmt.Sprintf("delivered %d/%d with ARQ isolated, but FEC can recover at most to %d (%d residual unrecoverable) — recovered %d packet(s) FEC cannot explain",
			in.Delivered, in.Total, ceiling, residual, in.Delivered-ceiling)
		return c
	}
	c.Verdict = result.Pass
	c.Detail = fmt.Sprintf("no over-recovery: %d recoverable, %d residual; delivered %d/%d, claimed %s",
		recoverable, residual, in.Delivered, in.Total, claim(in.ClaimedFEC))
	return c
}

// 3. fec-recovery-effective: the recoverable losses should actually have been
// recovered — delivery should reach the FEC ceiling (sent - residual). A
// shortfall is a WARN (FEC under-recovered the recoverable set; could be a
// late/dropped FEC packet or a non-conformant decode), not a hard FAIL, because
// delivery can also be shaped by timing and buffering on a real path.
func fecRecoveryEffective(in Input, recoverable, residual int) result.Check {
	c := result.Check{Name: "fec-recovery-effective"}
	if recoverable == 0 {
		c.Verdict = result.Pass
		c.Detail = "n/a (no recoverable loss to recover)"
		return c
	}
	ceiling := in.Total - residual
	if in.Delivered >= ceiling {
		c.Verdict = result.Pass
		c.Detail = fmt.Sprintf("FEC recovered the full recoverable set: delivered %d/%d (%d recovered, %d residual)",
			in.Delivered, in.Total, recoverable, residual)
	} else {
		c.Verdict = result.Warn
		c.Detail = fmt.Sprintf("FEC under-recovered: delivered %d/%d, FEC ceiling %d — %d recoverable loss(es) not delivered",
			in.Delivered, in.Total, ceiling, ceiling-in.Delivered)
	}
	return c
}

// 4. fec-active: recovery was exercised (some loss landed in the matrix). Clean
// runs report n/a so the row is honest about not testing recovery.
func fecActive(in Input, recoverable int) result.Check {
	c := result.Check{Name: "fec-active"}
	c.Verdict = result.Pass
	if n := countInBlock(in.Matrix, in.Lost); n > 0 {
		c.Detail = fmt.Sprintf("%d loss(es) in the FEC block; %d recoverable", n, recoverable)
	} else {
		c.Detail = "no loss in the FEC block (recovery not exercised)"
	}
	return c
}

func countInBlock(m Matrix, lost []int) int {
	n := 0
	for _, i := range lost {
		if i >= 0 && i < m.Block() {
			n++
		}
	}
	return n
}

func dim(twoD bool) string {
	if twoD {
		return "2-D"
	}
	return "1-D"
}

func claim(c int) string {
	if c < 0 {
		return "n/a"
	}
	return fmt.Sprintf("%d", c)
}
