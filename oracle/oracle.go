// Package oracle is the SRT protocol-invariant oracle: it turns the wire-level
// observations and run accounting into graded result.Checks that encode what
// "correct" SRT behavior means (handshake completed, delivery integrity, ARQ
// engaged under loss, ACK machinery healthy). It is the judging half of the
// moat — the part that decides Pass/Warn/Fail from the facts wire collected.
//
// The protocol-neutral graders (delivery integrity/completeness, signal/feedback
// presence) and the run-accounting Input live in the shared `grade` package;
// this package contributes only the SRT check roster and the genuinely
// SRT-specific graders (ARQ-under-loss, ACK monotonicity).
package oracle

import (
	"fmt"

	"github.com/zsiec/impair/grade"
	"github.com/zsiec/impair/result"
	"github.com/zsiec/impair/wire"
)

// Input is everything the SRT oracle needs to judge one (lib × scenario) run:
// the protocol-neutral run accounting (embedded grade.Input) plus the SRT wire
// Observation.
type Input struct {
	grade.Input
	Obs wire.Observation
}

// Evaluate runs every named oracle against the input and returns one
// result.Check per invariant, in a stable order.
func Evaluate(in Input) []result.Check {
	return []result.Check{
		grade.Presence("handshake-completed", in.Obs.Handshakes, in.SentMsgs > 0 || in.Opaque,
			"%d handshake packet(s) observed",
			"connection never established (no handshake observed despite a connection attempt)",
			"no handshake and no messages sent — run produced no evidence"),
		grade.DeliveryIntegrity(in.Input),
		grade.DeliveryComplete(in.Input),
		arqEngagedUnderLoss(in),
		ackMonotonic(in),
		grade.Feedback("ack-activity", in.Obs.ACKs,
			"%d ACK(s) observed",
			"no ACKs observed — connection may not have carried data"),
	}
}

// ResultFor wraps Evaluate into a result.Result for the given run.
func ResultFor(in Input) result.Result {
	return grade.Result(in.Input, Evaluate(in))
}

// arq-engaged-under-loss: under loss the ARQ machinery (NAK + retransmit) must
// have been exercised to recover delivery. SRT-specific: the ARQ signal requires
// BOTH a NAK and a retransmit (an opaque SUT is judged from wire facts alone).
func arqEngagedUnderLoss(in Input) result.Check {
	c := result.Check{Name: "arq-engaged-under-loss"}
	if !in.LossInjected {
		c.Verdict = result.Pass
		c.Detail = "n/a (no loss injected)"
		return c
	}
	if in.Opaque {
		// Wire-only judgement: no delivery counts, so we can confirm ARQ was
		// exercised but never Fail on completeness.
		switch {
		case in.Obs.RetransReqs > 0 && in.Obs.Retransmitted > 0:
			c.Verdict = result.Pass
			c.Detail = fmt.Sprintf("ARQ active: %d NAK(s), %d retransmit(s) (opaque SUT; delivery not measured)",
				in.Obs.RetransReqs, in.Obs.Retransmitted)
		case in.RelayDropped == 0:
			c.Verdict = result.Pass
			c.Detail = "no packets dropped on the wire (loss profile did not bite this run)"
		default:
			c.Verdict = result.Warn
			c.Detail = fmt.Sprintf("no ARQ activity under wire loss (%d dropped): %d NAK(s), %d retransmit(s)",
				in.RelayDropped, in.Obs.RetransReqs, in.Obs.Retransmitted)
		}
		return c
	}
	if in.SentMsgs == 0 {
		c.Verdict = result.Pass
		c.Detail = "n/a (no messages sent)"
		return c
	}

	arq := in.Obs.RetransReqs > 0 && in.Obs.Retransmitted > 0
	incomplete := in.DeliveredMsgs < int(float64(in.SentMsgs)*0.5)
	noActivity := in.Obs.RetransReqs == 0 && in.Obs.Retransmitted == 0

	switch {
	case arq && in.DeliveredMsgs > 0:
		c.Verdict = result.Pass
		c.Detail = fmt.Sprintf("ARQ recovered: %d NAK(s), %d retransmit(s), delivered %d/%d",
			in.Obs.RetransReqs, in.Obs.Retransmitted, in.DeliveredMsgs, in.SentMsgs)
	case noActivity && incomplete:
		c.Verdict = result.Fail
		c.Detail = fmt.Sprintf("no ARQ activity and delivery incomplete (%d/%d) under loss",
			in.DeliveredMsgs, in.SentMsgs)
	case noActivity:
		c.Verdict = result.Warn
		c.Detail = "no ARQ activity observed under loss (no NAKs, no retransmits)"
	default:
		// Partial ARQ activity (e.g. NAKs but no retransmits, or vice versa).
		c.Verdict = result.Warn
		c.Detail = fmt.Sprintf("partial ARQ activity under loss: %d NAK(s), %d retransmit(s), delivered %d/%d",
			in.Obs.RetransReqs, in.Obs.Retransmitted, in.DeliveredMsgs, in.SentMsgs)
	}
	return c
}

// ack-monotonic: ACK'd sequence numbers must never go backwards. SRT-specific
// (RIST has no graded ACK-ordering analogue).
func ackMonotonic(in Input) result.Check {
	c := result.Check{Name: "ack-monotonic"}
	if in.Obs.AckMonotonic {
		c.Verdict = result.Pass
		c.Detail = fmt.Sprintf("ACK sequence non-decreasing (max ack %d)", in.Obs.MaxAckSeq)
	} else {
		c.Verdict = result.Fail
		c.Detail = "ACK sequence went backwards"
	}
	return c
}
