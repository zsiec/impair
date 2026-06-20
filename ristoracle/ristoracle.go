// Package ristoracle is the RIST protocol-invariant oracle: the RIST analogue
// of the SRT `oracle` package. It turns the passive RIST wire observations
// (ristwire.Observation) and run accounting into graded result.Checks that
// encode what "correct" RIST Simple Profile behavior means — RTP media flowed,
// delivery was intact and complete, the RTCP-based ARQ (NACK -> retransmit)
// engaged under loss, and RTCP feedback was present. It is the judging half of
// the RIST moat, mirroring the SRT oracle's structure.
package ristoracle

import (
	"fmt"

	"github.com/zsiec/impair/result"
	"github.com/zsiec/impair/ristwire"
)

// Input is everything the RIST oracle needs to judge one (lib × scenario) run:
// the run accounting (messages sent/delivered/corrupt, whether loss was
// injected, relay drop count), the RIST wire Observation, and any precomputed
// Metrics.
type Input struct {
	Lib           string
	Scenario      string
	LossInjected  bool
	SentMsgs      int
	DeliveredMsgs int
	CorruptMsgs   int
	RelayDropped  uint64
	Obs           ristwire.Observation
	// Opaque marks a black-box SUT whose application-level delivery/integrity we
	// cannot measure. For these, the delivery oracles report n/a and ARQ is
	// judged from wire facts alone — never failing on completeness.
	Opaque  bool
	Metrics map[string]float64
}

// Evaluate runs every named RIST oracle against the input and returns one
// result.Check per invariant, in a stable order.
func Evaluate(in Input) []result.Check {
	return []result.Check{
		rtpFlow(in),
		deliveryIntegrity(in),
		deliveryComplete(in),
		retransmitUnderLoss(in),
		rtcpActivity(in),
	}
}

// ResultFor wraps Evaluate into a result.Result for the given run.
func ResultFor(in Input) result.Result {
	return result.Result{
		Lib:      in.Lib,
		Scenario: in.Scenario,
		Checks:   Evaluate(in),
		Metrics:  in.Metrics,
	}
}

// 1. rtp-flow: RTP media must have flowed for a stream to exist.
func rtpFlow(in Input) result.Check {
	c := result.Check{Name: "rtp-flow"}
	switch {
	case in.Obs.RTPPackets > 0:
		c.Verdict = result.Pass
		c.Detail = fmt.Sprintf("%d RTP packet(s) observed (media flowed)", in.Obs.RTPPackets)
	case in.SentMsgs > 0 || in.Opaque:
		c.Verdict = result.Fail
		c.Detail = "no RTP observed despite a connection attempt"
	default:
		c.Verdict = result.Error
		c.Detail = "no RTP and no messages sent — run produced no evidence"
	}
	return c
}

// 2. delivery-integrity: no message may be delivered corrupted.
func deliveryIntegrity(in Input) result.Check {
	c := result.Check{Name: "delivery-integrity"}
	if in.Opaque {
		c.Verdict = result.Pass
		c.Detail = "n/a (opaque SUT — application delivery not measured)"
		return c
	}
	if in.CorruptMsgs == 0 {
		c.Verdict = result.Pass
		c.Detail = "no corrupt messages delivered"
	} else {
		c.Verdict = result.Fail
		c.Detail = fmt.Sprintf("%d corrupt message(s) delivered", in.CorruptMsgs)
	}
	return c
}

// 3. delivery-complete: without loss every message must arrive; with loss the
// ratio is reported but completeness is not asserted (live mode tolerates loss).
func deliveryComplete(in Input) result.Check {
	c := result.Check{Name: "delivery-complete"}
	if in.Opaque {
		c.Verdict = result.Pass
		c.Detail = "n/a (opaque SUT — application delivery not measured)"
		return c
	}
	if in.LossInjected {
		c.Verdict = result.Pass
		c.Detail = fmt.Sprintf("delivered %d/%d (loss injected; completeness not asserted under loss)",
			in.DeliveredMsgs, in.SentMsgs)
		return c
	}
	if in.DeliveredMsgs >= in.SentMsgs {
		c.Verdict = result.Pass
		c.Detail = fmt.Sprintf("delivered %d/%d", in.DeliveredMsgs, in.SentMsgs)
	} else {
		c.Verdict = result.Fail
		c.Detail = fmt.Sprintf("delivered %d/%d (incomplete with no loss injected)",
			in.DeliveredMsgs, in.SentMsgs)
	}
	return c
}

// 4. retransmit-under-loss: under loss the RIST ARQ machinery (RTCP NACK -> RTP
// retransmit) must have been exercised to recover lost packets.
func retransmitUnderLoss(in Input) result.Check {
	c := result.Check{Name: "retransmit-under-loss"}
	if !in.LossInjected {
		c.Verdict = result.Pass
		c.Detail = "n/a (no loss)"
		return c
	}

	// Retransmissions observed on the wire (a sequence number seen again) are the
	// implementation-AGNOSTIC ARQ signal: different RIST stacks request retransmits
	// differently (RFC 4585 RTPFB vs a vendor RTCP-APP range-NACK, e.g. ristgo's
	// "RIST" APP packet), so we key on retransmits, not on the NACK encoding.
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

// 5. rtcp-activity: a healthy RIST stream should carry RTCP feedback (sender/
// receiver reports, NACKs).
func rtcpActivity(in Input) result.Check {
	c := result.Check{Name: "rtcp-activity"}
	if in.Obs.RTCPPackets > 0 {
		c.Verdict = result.Pass
		c.Detail = fmt.Sprintf("%d RTCP packet(s) observed", in.Obs.RTCPPackets)
	} else {
		c.Verdict = result.Warn
		c.Detail = "no RTCP feedback observed"
	}
	return c
}
