// Package oracle is the SRT protocol-invariant oracle: it turns the wire-level
// observations and run accounting into graded result.Checks that encode what
// "correct" SRT behavior means (handshake completed, delivery integrity, ARQ
// engaged under loss, ACK machinery healthy). It is the judging half of the
// moat — the part that decides Pass/Warn/Fail from the facts wire collected.
package oracle

import (
	"fmt"

	"github.com/zsiec/impair/result"
	"github.com/zsiec/impair/wire"
)

// Input is everything the oracle needs to judge one (lib × scenario) run: the
// run accounting (messages sent/delivered/corrupt, whether loss was injected,
// relay drop count), the wire Observation, and any precomputed Metrics.
type Input struct {
	Lib           string
	Scenario      string
	LossInjected  bool
	SentMsgs      int
	DeliveredMsgs int
	CorruptMsgs   int
	RelayDropped  uint64
	Obs           wire.Observation
	Metrics       map[string]float64
}

// Evaluate runs every named oracle against the input and returns one
// result.Check per invariant, in a stable order.
func Evaluate(in Input) []result.Check {
	return []result.Check{
		handshakeCompleted(in),
		deliveryIntegrity(in),
		deliveryComplete(in),
		arqEngagedUnderLoss(in),
		ackMonotonic(in),
		ackActivity(in),
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

// 1. handshake-completed: a connection must have been established.
func handshakeCompleted(in Input) result.Check {
	c := result.Check{Name: "handshake-completed"}
	switch {
	case in.Obs.Handshakes > 0:
		c.Verdict = result.Pass
		c.Detail = fmt.Sprintf("%d handshake packet(s) observed", in.Obs.Handshakes)
	case in.SentMsgs > 0:
		c.Verdict = result.Fail
		c.Detail = "connection never established (no handshake observed despite traffic)"
	default:
		c.Verdict = result.Error
		c.Detail = "no handshake and no messages sent — run produced no evidence"
	}
	return c
}

// 2. delivery-integrity: no message may be delivered corrupted.
func deliveryIntegrity(in Input) result.Check {
	c := result.Check{Name: "delivery-integrity"}
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

// 4. arq-engaged-under-loss: under loss the ARQ machinery (NAK + retransmit)
// must have been exercised to recover delivery.
func arqEngagedUnderLoss(in Input) result.Check {
	c := result.Check{Name: "arq-engaged-under-loss"}
	if !in.LossInjected || in.SentMsgs == 0 {
		c.Verdict = result.Pass
		c.Detail = "n/a (no loss injected)"
		return c
	}

	arq := in.Obs.NAKs > 0 && in.Obs.Retransmitted > 0
	incomplete := in.DeliveredMsgs < int(float64(in.SentMsgs)*0.5)
	noActivity := in.Obs.NAKs == 0 && in.Obs.Retransmitted == 0

	switch {
	case arq && in.DeliveredMsgs > 0:
		c.Verdict = result.Pass
		c.Detail = fmt.Sprintf("ARQ recovered: %d NAK(s), %d retransmit(s), delivered %d/%d",
			in.Obs.NAKs, in.Obs.Retransmitted, in.DeliveredMsgs, in.SentMsgs)
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
			in.Obs.NAKs, in.Obs.Retransmitted, in.DeliveredMsgs, in.SentMsgs)
	}
	return c
}

// 5. ack-monotonic: ACK'd sequence numbers must never go backwards.
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

// 6. ack-activity: a healthy data connection should produce ACKs.
func ackActivity(in Input) result.Check {
	c := result.Check{Name: "ack-activity"}
	if in.Obs.ACKs > 0 {
		c.Verdict = result.Pass
		c.Detail = fmt.Sprintf("%d ACK(s) observed", in.Obs.ACKs)
	} else {
		c.Verdict = result.Warn
		c.Detail = "no ACKs observed — connection may not have carried data"
	}
	return c
}
