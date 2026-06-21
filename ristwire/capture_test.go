package ristwire

import (
	"encoding/hex"
	"testing"
)

// These are real datagrams captured from a ristgo (github.com/zsiec/ristgo)
// Simple-profile loopback on 2026-06-20, via a dual-port UDP relay that dropped
// ~1-in-7 media datagrams to force retransmission. They validate the decoder
// against on-the-wire bytes rather than only hand-built ones.
//
// Capture findings (reported alongside the package):
//   - ristgo uses the Simple-profile TWO-PORT scheme: RTP media on even port P,
//     compound RTCP on odd port P+1 (NOT multiplexed on one port).
//   - Media is RTP with payload type 33 (MP2T): byte1 = 0x21.
//   - The receiver's RTCP is a COMPOUND packet: RR (PT=201) + SDES (PT=202) +
//     an RTCP APP packet (PT=204) named "RIST" whose body is the list of
//     sequence numbers to retransmit. ristgo's retransmit request is thus a
//     vendor RTCP-APP range-NACK, NOT the RFC 4585 RTPFB (PT=205) Generic NACK.
//     Our decoder still classifies it correctly as an RTCP packet; the RFC 4585
//     RTPFB path (the contract's NACK) is exercised by the hand-built tests.
const (
	realRTP     = "8021cf3d000000be6500c648"         // RTP: PT=33, seq=0xcf3d, ts=0xbe, SSRC=0x6500c648
	realRTCPSR  = "80c800066500c648"                 // SR:  PT=200, SSRC=0x6500c648
	realRTCPRR  = "80c90001204298fe"                 // RR:  PT=201, SSRC=0x204298fe
	realRISTAPP = "80cc00036500c64852495354cfb30000" // APP: PT=204, name "RIST"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex %q: %v", s, err)
	}
	return b
}

func TestDecodeRealRTP(t *testing.T) {
	p, ok := Decode(mustHex(t, realRTP))
	if !ok {
		t.Fatal("Decode failed on real RTP")
	}
	if p.IsRTCP {
		t.Error("real RTP misclassified as RTCP")
	}
	if p.PayloadType != 33 {
		t.Errorf("PayloadType = %d, want 33 (MP2T)", p.PayloadType)
	}
	if p.Seq != 0xcf3d {
		t.Errorf("Seq = %#x, want 0xcf3d", p.Seq)
	}
	if p.SSRC != 0x6500c648 {
		t.Errorf("SSRC = %#x, want 0x6500c648", p.SSRC)
	}
}

func TestDecodeRealRTCP(t *testing.T) {
	cases := []struct {
		name string
		hex  string
		pt   uint8
		ssrc uint32
	}{
		{"SR", realRTCPSR, RTCPSenderReport, 0x6500c648},
		{"RR", realRTCPRR, RTCPReceiverReport, 0x204298fe},
		{"RIST-APP", realRISTAPP, RTCPApp, 0x6500c648},
	}
	for _, c := range cases {
		p, ok := Decode(mustHex(t, c.hex))
		if !ok {
			t.Fatalf("%s: Decode failed", c.name)
		}
		if !p.IsRTCP {
			t.Errorf("%s: IsRTCP = false, want true", c.name)
		}
		if p.PayloadType != c.pt {
			t.Errorf("%s: PayloadType = %d, want %d", c.name, p.PayloadType, c.pt)
		}
		if p.SSRC != c.ssrc {
			t.Errorf("%s: SSRC = %#x, want %#x", c.name, p.SSRC, c.ssrc)
		}
	}
}

// TestObserveRealCapture replays a slice of the real capture and checks the
// observer's tallies: the RIST-APP retransmit request counts as an RTCP packet
// (it is not an RFC 4585 RTPFB, so it does not increment RetransReqs), and a media
// sequence number seen twice is counted as a retransmission.
func TestObserveRealCapture(t *testing.T) {
	o := NewObserver()
	o.Observe(mustHex(t, realRTCPSR))
	o.Observe(mustHex(t, realRTP))
	o.Observe(mustHex(t, realRTCPRR))
	o.Observe(mustHex(t, realRISTAPP))
	o.Observe(mustHex(t, realRTP)) // same seq again = retransmit

	obs := o.Observation()
	if obs.DataPackets != 2 {
		t.Errorf("DataPackets = %d, want 2", obs.DataPackets)
	}
	if obs.ControlPackets != 3 {
		t.Errorf("ControlPackets = %d, want 3 (SR, RR, APP)", obs.ControlPackets)
	}
	if obs.SenderReports != 1 {
		t.Errorf("SenderReports = %d, want 1", obs.SenderReports)
	}
	if obs.ReceiverReports != 1 {
		t.Errorf("ReceiverReports = %d, want 1", obs.ReceiverReports)
	}
	if obs.RetransReqs != 0 {
		t.Errorf("RetransReqs = %d, want 0 (RIST-APP is not RFC 4585 RTPFB)", obs.RetransReqs)
	}
	if obs.Retransmitted != 1 || !obs.RetransSeqs[0xcf3d] {
		t.Errorf("retransmit of seq 0xcf3d not detected: Retransmitted=%d", obs.Retransmitted)
	}
}
