// Package ristoracle is the RIST protocol-invariant oracle: the RIST analogue
// of the SRT `oracle` package. It turns the passive RIST wire observations
// (ristwire.Observation) and run accounting into graded result.Checks that
// encode what "correct" RIST Simple Profile behavior means — RTP media flowed,
// delivery was intact and complete, the RTCP-based ARQ (NACK -> retransmit)
// engaged under loss, and RTCP feedback was present.
//
// The protocol-neutral graders (delivery integrity/completeness, signal/feedback
// presence) and the run-accounting Input live in the shared `grade` package;
// this package contributes only the RIST check roster and the genuinely
// RIST-specific retransmit-under-loss grader.
package ristoracle

import (
	"fmt"

	"github.com/zsiec/impair/grade"
	"github.com/zsiec/impair/result"
	"github.com/zsiec/impair/ristwire"
)

// Input is everything the RIST oracle needs to judge one (lib × scenario) run:
// the protocol-neutral run accounting (embedded grade.Input) plus the RIST wire
// Observation.
type Input struct {
	grade.Input
	Obs ristwire.Observation
}

// Evaluate runs every named RIST oracle against the input and returns one
// result.Check per invariant, in a stable order.
func Evaluate(in Input) []result.Check {
	return []result.Check{
		grade.Presence("rtp-flow", in.Obs.RTPPackets, in.SentMsgs > 0 || in.Opaque,
			"%d RTP packet(s) observed (media flowed)",
			"no RTP observed despite a connection attempt",
			"no RTP and no messages sent — run produced no evidence"),
		grade.DeliveryIntegrity(in.Input),
		grade.DeliveryComplete(in.Input),
		retransmitUnderLoss(in),
		grade.Feedback("rtcp-activity", in.Obs.RTCPPackets,
			"%d RTCP packet(s) observed",
			"no RTCP feedback observed"),
	}
}

// ResultFor wraps Evaluate into a result.Result for the given run.
func ResultFor(in Input) result.Result {
	return grade.Result(in.Input, Evaluate(in))
}

// retransmit-under-loss: under loss the RIST ARQ machinery (RTCP NACK -> RTP
// retransmit) must have been exercised to recover lost packets. RIST-specific:
// the ARQ signal is retransmits-on-the-wire ALONE (not the NACK encoding),
// because different RIST stacks request retransmits differently (RFC 4585 RTPFB
// vs a vendor RTCP-APP range-NACK, e.g. ristgo's "RIST" APP packet).
func retransmitUnderLoss(in Input) result.Check {
	c := result.Check{Name: "retransmit-under-loss"}
	if !in.LossInjected {
		c.Verdict = result.Pass
		c.Detail = "n/a (no loss)"
		return c
	}

	arq := in.Obs.Retransmitted > 0
	noActivity := in.Obs.Retransmitted == 0

	switch {
	case arq:
		c.Verdict = result.Pass
		c.Detail = fmt.Sprintf("RIST ARQ engaged: %d retransmit(s) observed (%d RFC-4585 NACK(s))",
			in.Obs.Retransmitted, in.Obs.NACKs)
	case in.RelayDropped == 0:
		c.Verdict = result.Pass
		c.Detail = "loss profile did not bite (no packets dropped on the wire)"
	case noActivity && !in.Opaque && in.DeliveredMsgs < in.SentMsgs/2:
		// Measurable SUT: no recovery and delivery clearly incomplete.
		c.Verdict = result.Fail
		c.Detail = fmt.Sprintf("no ARQ activity and delivery incomplete (%d/%d) under loss (%d dropped)",
			in.DeliveredMsgs, in.SentMsgs, in.RelayDropped)
	default:
		// Partial or no ARQ activity. For opaque SUTs we never have delivery
		// data, so we can only Warn here.
		c.Verdict = result.Warn
		c.Detail = fmt.Sprintf("partial/no ARQ activity under loss (%d dropped): %d NACK(s), %d retransmit(s)",
			in.RelayDropped, in.Obs.NACKs, in.Obs.Retransmitted)
	}
	return c
}
