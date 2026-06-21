package oracle

import (
	"testing"

	"github.com/zsiec/impair/grade"
	"github.com/zsiec/impair/result"
	"github.com/zsiec/impair/wire"
)

// find returns the Check with the given name, failing the test if absent.
func find(t *testing.T, checks []result.Check, name string) result.Check {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no check named %q in %v", name, checks)
	return result.Check{}
}

func assertVerdict(t *testing.T, in Input, name string, want result.Verdict) {
	t.Helper()
	c := find(t, Evaluate(in), name)
	if c.Verdict != want {
		t.Errorf("%s: verdict = %v, want %v (detail: %q)", name, c.Verdict, want, c.Detail)
	}
}

// base returns a clean, fully-healthy input that passes every oracle.
func base() Input {
	return Input{
		Input: grade.Input{
			Lib:           "lib",
			Scenario:      "scn",
			LossInjected:  false,
			SentMsgs:      100,
			DeliveredMsgs: 100,
			CorruptMsgs:   0,
			RelayDropped:  0,
		},
		Obs: wire.Observation{
			Handshakes:   2,
			ACKs:         10,
			AckMonotonic: true,
		},
	}
}

func TestEvaluateAllPass(t *testing.T) {
	for _, c := range Evaluate(base()) {
		if c.Verdict != result.Pass {
			t.Errorf("check %s: verdict = %v, want Pass (detail %q)", c.Name, c.Verdict, c.Detail)
		}
	}
}

func TestHandshakeCompleted(t *testing.T) {
	// Pass: handshakes observed.
	assertVerdict(t, base(), "handshake-completed", result.Pass)

	// Fail: traffic sent but no handshake.
	in := base()
	in.Obs.Handshakes = 0
	assertVerdict(t, in, "handshake-completed", result.Fail)

	// Error: no handshake and nothing sent.
	in = base()
	in.Obs.Handshakes = 0
	in.SentMsgs = 0
	assertVerdict(t, in, "handshake-completed", result.Error)
}

func TestDeliveryIntegrity(t *testing.T) {
	assertVerdict(t, base(), "delivery-integrity", result.Pass)

	in := base()
	in.CorruptMsgs = 3
	assertVerdict(t, in, "delivery-integrity", result.Fail)
}

func TestDeliveryComplete(t *testing.T) {
	// Pass: no loss, all delivered.
	assertVerdict(t, base(), "delivery-complete", result.Pass)

	// Fail: no loss, incomplete.
	in := base()
	in.DeliveredMsgs = 90
	assertVerdict(t, in, "delivery-complete", result.Fail)

	// Pass: loss injected, completeness not asserted even when incomplete.
	in = base()
	in.LossInjected = true
	in.DeliveredMsgs = 70
	c := find(t, Evaluate(in), "delivery-complete")
	if c.Verdict != result.Pass {
		t.Errorf("delivery-complete under loss: verdict = %v, want Pass", c.Verdict)
	}
	if c.Detail == "" {
		t.Errorf("delivery-complete under loss: expected delivery ratio in Detail")
	}
}

func TestArqEngagedUnderLoss(t *testing.T) {
	// Pass (n/a): no loss injected.
	c := find(t, Evaluate(base()), "arq-engaged-under-loss")
	if c.Verdict != result.Pass || c.Detail != "n/a (no loss injected)" {
		t.Errorf("no-loss: got %v / %q", c.Verdict, c.Detail)
	}

	// Pass: ARQ recovered (NAKs + retransmits + delivery).
	in := base()
	in.LossInjected = true
	in.RelayDropped = 5
	in.DeliveredMsgs = 95
	in.Obs.NAKs = 5
	in.Obs.Retransmitted = 5
	assertVerdict(t, in, "arq-engaged-under-loss", result.Pass)

	// Warn: loss injected but no ARQ activity, delivery still mostly fine.
	in = base()
	in.LossInjected = true
	in.DeliveredMsgs = 99
	in.Obs.NAKs = 0
	in.Obs.Retransmitted = 0
	assertVerdict(t, in, "arq-engaged-under-loss", result.Warn)

	// Fail: no ARQ activity AND delivery clearly incomplete (<50%).
	in = base()
	in.LossInjected = true
	in.SentMsgs = 100
	in.DeliveredMsgs = 40
	in.Obs.NAKs = 0
	in.Obs.Retransmitted = 0
	assertVerdict(t, in, "arq-engaged-under-loss", result.Fail)

	// Warn: partial ARQ activity (NAKs but no retransmits).
	in = base()
	in.LossInjected = true
	in.DeliveredMsgs = 95
	in.Obs.NAKs = 5
	in.Obs.Retransmitted = 0
	assertVerdict(t, in, "arq-engaged-under-loss", result.Warn)
}

func TestAckMonotonic(t *testing.T) {
	assertVerdict(t, base(), "ack-monotonic", result.Pass)

	in := base()
	in.Obs.AckMonotonic = false
	assertVerdict(t, in, "ack-monotonic", result.Fail)
}

func TestAckActivity(t *testing.T) {
	assertVerdict(t, base(), "ack-activity", result.Pass)

	in := base()
	in.Obs.ACKs = 0
	assertVerdict(t, in, "ack-activity", result.Warn)
}

func TestResultForRollup(t *testing.T) {
	// All-pass input rolls up to Pass.
	in := base()
	in.Metrics = map[string]float64{"deliveryPct": 100}
	r := ResultFor(in)
	if r.Lib != "lib" || r.Scenario != "scn" {
		t.Errorf("ResultFor identity = %q/%q", r.Lib, r.Scenario)
	}
	if r.Metrics["deliveryPct"] != 100 {
		t.Errorf("ResultFor dropped metrics: %v", r.Metrics)
	}
	if v := r.Verdict(); v != result.Pass {
		t.Errorf("all-pass rollup = %v, want Pass", v)
	}

	// A Warn check makes the rollup Warn.
	in = base()
	in.Obs.ACKs = 0 // ack-activity -> Warn
	if v := ResultFor(in).Verdict(); v != result.Warn {
		t.Errorf("warn rollup = %v, want Warn", v)
	}

	// A Fail check dominates a Warn.
	in = base()
	in.Obs.ACKs = 0    // Warn
	in.CorruptMsgs = 1 // Fail
	if v := ResultFor(in).Verdict(); v != result.Fail {
		t.Errorf("fail rollup = %v, want Fail", v)
	}

	// Error dominates (no handshake, nothing sent).
	in = base()
	in.Obs.Handshakes = 0
	in.SentMsgs = 0
	in.DeliveredMsgs = 0
	if v := ResultFor(in).Verdict(); v != result.Error {
		t.Errorf("error rollup = %v, want Error", v)
	}
}

// Opaque (black-box) SUTs: delivery oracles are n/a; wire oracles still apply.
func TestOpaqueWireOnly(t *testing.T) {
	in := base()
	in.Opaque = true
	in.LossInjected = true
	in.SentMsgs = 0
	in.DeliveredMsgs = 0
	in.RelayDropped = 50
	in.Obs.NAKs = 10
	in.Obs.Retransmitted = 25
	checks := byName(Evaluate(in))
	if got := checks["delivery-integrity"].Verdict; got != result.Pass {
		t.Errorf("delivery-integrity opaque = %v, want Pass (n/a)", got)
	}
	if got := checks["delivery-complete"].Verdict; got != result.Pass {
		t.Errorf("delivery-complete opaque = %v, want Pass (n/a)", got)
	}
	if got := checks["arq-engaged-under-loss"].Verdict; got != result.Pass {
		t.Errorf("arq opaque w/ NAK+retx = %v, want Pass", got)
	}
	// Opaque with loss but no ARQ activity -> Warn (cannot Fail without delivery data).
	in.Obs.NAKs, in.Obs.Retransmitted = 0, 0
	if got := byName(Evaluate(in))["arq-engaged-under-loss"].Verdict; got != result.Warn {
		t.Errorf("arq opaque no-activity = %v, want Warn", got)
	}
	// Opaque with no handshake observed -> Fail (a connection was attempted).
	in.Obs.Handshakes = 0
	if got := byName(Evaluate(in))["handshake-completed"].Verdict; got != result.Fail {
		t.Errorf("handshake opaque none = %v, want Fail", got)
	}
}

func byName(cs []result.Check) map[string]result.Check {
	m := map[string]result.Check{}
	for _, c := range cs {
		m[c.Name] = c
	}
	return m
}
