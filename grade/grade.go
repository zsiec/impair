// Package grade is the protocol-agnostic grading toolkit shared by the
// per-protocol oracles (SRT `oracle`, RIST `ristoracle`, and any future ones).
// It owns the run-accounting Input, the result.Check construction helper, the
// graders whose logic is identical across protocols (delivery integrity and
// completeness), and the parameterized templates for the patterns that recur
// with a protocol-specific signal (presence of an essential signal, presence of
// feedback). Each protocol oracle embeds Input, supplies its own check roster,
// and keeps only the genuinely protocol-specific graders (ARQ-under-loss and the
// like). This is what lets a new protocol slot in without re-deriving the
// boilerplate.
package grade

import (
	"fmt"

	"github.com/zsiec/impair/result"
)

// Input is the protocol-neutral run accounting every oracle judges against: how
// many messages were sent/delivered/corrupt, whether loss was injected and how
// much the relay dropped, whether the SUT is opaque (black-box), and any
// precomputed metrics. The protocol-specific wire Observation lives on each
// oracle's own Input, which embeds this.
type Input struct {
	Lib           string
	Scenario      string
	LossInjected  bool
	SentMsgs      int
	DeliveredMsgs int
	CorruptMsgs   int
	RelayDropped  uint64
	// Opaque marks a black-box SUT whose application-level delivery/integrity we
	// cannot measure; the delivery graders report n/a and ARQ is judged from wire
	// facts alone, never failing on completeness.
	Opaque  bool
	Metrics map[string]float64
}

// Checkf builds a result.Check with a printf-formatted detail. It is the single
// Check-construction helper the oracles use instead of the repeated
// `c := result.Check{Name: ...}; c.Verdict = ...; c.Detail = fmt.Sprintf(...)`.
func Checkf(name string, v result.Verdict, format string, a ...any) result.Check {
	return result.Check{Name: name, Verdict: v, Detail: fmt.Sprintf(format, a...)}
}

// Result packs a protocol oracle's checks into a result.Result for the run.
func Result(in Input, checks []result.Check) result.Result {
	return result.Result{Lib: in.Lib, Scenario: in.Scenario, Checks: checks, Metrics: in.Metrics}
}

// DeliveryIntegrity grades that no message was delivered corrupted. Identical
// across protocols; n/a for opaque SUTs (application delivery not measured).
func DeliveryIntegrity(in Input) result.Check {
	const name = "delivery-integrity"
	switch {
	case in.Opaque:
		return Checkf(name, result.Pass, "n/a (opaque SUT — application delivery not measured)")
	case in.CorruptMsgs == 0:
		return Checkf(name, result.Pass, "no corrupt messages delivered")
	default:
		return Checkf(name, result.Fail, "%d corrupt message(s) delivered", in.CorruptMsgs)
	}
}

// DeliveryComplete grades message completeness: without loss every message must
// arrive; with loss the ratio is reported but completeness is not asserted (live
// mode tolerates loss). Identical across protocols; n/a for opaque SUTs.
func DeliveryComplete(in Input) result.Check {
	const name = "delivery-complete"
	switch {
	case in.Opaque:
		return Checkf(name, result.Pass, "n/a (opaque SUT — application delivery not measured)")
	case in.LossInjected:
		return Checkf(name, result.Pass, "delivered %d/%d (loss injected; completeness not asserted under loss)",
			in.DeliveredMsgs, in.SentMsgs)
	case in.DeliveredMsgs >= in.SentMsgs:
		return Checkf(name, result.Pass, "delivered %d/%d", in.DeliveredMsgs, in.SentMsgs)
	default:
		return Checkf(name, result.Fail, "delivered %d/%d (incomplete with no loss injected)",
			in.DeliveredMsgs, in.SentMsgs)
	}
}

// Presence grades a "did the session produce its essential signal" check (SRT
// handshake-completed, RIST rtp-flow): present (observed > 0) is a Pass; absent
// when delivery was attempted is a Fail; absent with no evidence at all is an
// Error. presentFmt takes the observed count; absent and noEvidence are plain
// protocol-specific detail strings.
func Presence(name string, observed int, attempted bool, presentFmt, absent, noEvidence string) result.Check {
	switch {
	case observed > 0:
		return Checkf(name, result.Pass, presentFmt, observed)
	case attempted:
		return Checkf(name, result.Fail, "%s", absent)
	default:
		return Checkf(name, result.Error, "%s", noEvidence)
	}
}

// Feedback grades the presence of a healthy feedback channel (SRT ack-activity,
// RIST rtcp-activity): present is a Pass, absent is a Warn (the connection may
// simply not have carried data). presentFmt takes the observed count.
func Feedback(name string, observed int, presentFmt, absent string) result.Check {
	if observed > 0 {
		return Checkf(name, result.Pass, presentFmt, observed)
	}
	return Checkf(name, result.Warn, "%s", absent)
}
