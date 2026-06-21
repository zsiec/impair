package ristoracle

import (
	"testing"

	"github.com/zsiec/impair/grade"
	"github.com/zsiec/impair/result"
	"github.com/zsiec/impair/ristwire"
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

func byName(cs []result.Check) map[string]result.Check {
	m := map[string]result.Check{}
	for _, c := range cs {
		m[c.Name] = c
	}
	return m
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
		Obs: ristwire.Observation{
			RTPPackets:    100,
			RTCPPackets:   10,
			SenderReports: 5,
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

func TestRTPFlow(t *testing.T) {
	// Pass: RTP observed.
	assertVerdict(t, base(), "rtp-flow", result.Pass)

	// Fail: messages sent but no RTP.
	in := base()
	in.Obs.RTPPackets = 0
	assertVerdict(t, in, "rtp-flow", result.Fail)

	// Fail: opaque, no RTP (a connection was attempted).
	in = base()
	in.Obs.RTPPackets = 0
	in.SentMsgs = 0
	in.Opaque = true
	assertVerdict(t, in, "rtp-flow", result.Fail)

	// Error: no RTP and nothing sent.
	in = base()
	in.Obs.RTPPackets = 0
	in.SentMsgs = 0
	assertVerdict(t, in, "rtp-flow", result.Error)
}

func TestDeliveryIntegrity(t *testing.T) {
	assertVerdict(t, base(), "delivery-integrity", result.Pass)

	in := base()
	in.CorruptMsgs = 3
	assertVerdict(t, in, "delivery-integrity", result.Fail)

	// Opaque: n/a -> Pass even with corruption count set.
	in = base()
	in.Opaque = true
	in.CorruptMsgs = 3
	assertVerdict(t, in, "delivery-integrity", result.Pass)
}

func TestDeliveryComplete(t *testing.T) {
	// Pass: no loss, all delivered.
	assertVerdict(t, base(), "delivery-complete", result.Pass)

	// Fail: no loss, incomplete.
	in := base()
	in.DeliveredMsgs = 90
	assertVerdict(t, in, "delivery-complete", result.Fail)

	// Pass: loss injected, completeness not asserted; ratio in Detail.
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

	// Pass: opaque -> n/a.
	in = base()
	in.Opaque = true
	in.DeliveredMsgs = 0
	assertVerdict(t, in, "delivery-complete", result.Pass)
}

func TestRetransmitUnderLoss(t *testing.T) {
	// Pass (n/a): no loss injected.
	c := find(t, Evaluate(base()), "retransmit-under-loss")
	if c.Verdict != result.Pass || c.Detail != "n/a (no loss)" {
		t.Errorf("no-loss: got %v / %q", c.Verdict, c.Detail)
	}

	// Pass: ARQ engaged (NACKs + retransmits).
	in := base()
	in.LossInjected = true
	in.RelayDropped = 5
	in.DeliveredMsgs = 95
	in.Obs.NACKs = 5
	in.Obs.Retransmitted = 5
	assertVerdict(t, in, "retransmit-under-loss", result.Pass)

	// Pass: loss profile did not bite (RelayDropped == 0).
	in = base()
	in.LossInjected = true
	in.RelayDropped = 0
	in.Obs.NACKs = 0
	in.Obs.Retransmitted = 0
	assertVerdict(t, in, "retransmit-under-loss", result.Pass)

	// Fail: measurable SUT, no ARQ activity, delivery clearly incomplete (<50%).
	in = base()
	in.LossInjected = true
	in.RelayDropped = 50
	in.SentMsgs = 100
	in.DeliveredMsgs = 40
	in.Obs.NACKs = 0
	in.Obs.Retransmitted = 0
	assertVerdict(t, in, "retransmit-under-loss", result.Fail)

	// Warn: partial activity (NACKs but no retransmits) under real drops.
	in = base()
	in.LossInjected = true
	in.RelayDropped = 5
	in.DeliveredMsgs = 95
	in.Obs.NACKs = 5
	in.Obs.Retransmitted = 0
	assertVerdict(t, in, "retransmit-under-loss", result.Warn)

	// Warn: no activity but delivery still mostly fine (>=50%).
	in = base()
	in.LossInjected = true
	in.RelayDropped = 5
	in.DeliveredMsgs = 99
	in.Obs.NACKs = 0
	in.Obs.Retransmitted = 0
	assertVerdict(t, in, "retransmit-under-loss", result.Warn)
}

func TestRTCPActivity(t *testing.T) {
	assertVerdict(t, base(), "rtcp-activity", result.Pass)

	in := base()
	in.Obs.RTCPPackets = 0
	assertVerdict(t, in, "rtcp-activity", result.Warn)
}

// Opaque (black-box) SUTs: delivery oracles are n/a; wire oracles still apply,
// and retransmit-under-loss never Fails (no delivery data) — only Warn.
func TestOpaqueWireOnly(t *testing.T) {
	in := base()
	in.Opaque = true
	in.LossInjected = true
	in.SentMsgs = 0
	in.DeliveredMsgs = 0
	in.RelayDropped = 50
	in.Obs.NACKs = 10
	in.Obs.Retransmitted = 25
	checks := byName(Evaluate(in))
	if got := checks["delivery-integrity"].Verdict; got != result.Pass {
		t.Errorf("delivery-integrity opaque = %v, want Pass (n/a)", got)
	}
	if got := checks["delivery-complete"].Verdict; got != result.Pass {
		t.Errorf("delivery-complete opaque = %v, want Pass (n/a)", got)
	}
	if got := checks["retransmit-under-loss"].Verdict; got != result.Pass {
		t.Errorf("retransmit opaque w/ NACK+retx = %v, want Pass", got)
	}

	// Opaque, loss, drops bit, but no ARQ activity -> Warn (never Fail).
	in.Obs.NACKs, in.Obs.Retransmitted = 0, 0
	if got := byName(Evaluate(in))["retransmit-under-loss"].Verdict; got != result.Warn {
		t.Errorf("retransmit opaque no-activity = %v, want Warn", got)
	}
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
	in.Obs.RTCPPackets = 0 // rtcp-activity -> Warn
	if v := ResultFor(in).Verdict(); v != result.Warn {
		t.Errorf("warn rollup = %v, want Warn", v)
	}

	// A Fail check dominates a Warn.
	in = base()
	in.Obs.RTCPPackets = 0 // Warn
	in.CorruptMsgs = 1     // Fail
	if v := ResultFor(in).Verdict(); v != result.Fail {
		t.Errorf("fail rollup = %v, want Fail", v)
	}

	// Error dominates (no RTP, nothing sent).
	in = base()
	in.Obs.RTPPackets = 0
	in.SentMsgs = 0
	in.DeliveredMsgs = 0
	if v := ResultFor(in).Verdict(); v != result.Error {
		t.Errorf("error rollup = %v, want Error", v)
	}
}
