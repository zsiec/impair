package wire

import (
	"testing"

	"github.com/zsiec/impair/engine"
)

func ctrl(ctrlType uint16, cif ...uint32) []byte {
	return append(be(ctrlWord0(ctrlType), 0, 0, 0), be(cif...)...)
}

func dataPkt(seq uint32, retrans bool) []byte {
	w1 := uint32(0xE0000000)
	if retrans {
		w1 |= retransMask
	}
	return be(seq, w1, 0, 0)
}

func TestObserverAccumulate(t *testing.T) {
	o := NewObserver()

	o.Observe(engine.C2S, dataPkt(1, false))
	o.Observe(engine.C2S, dataPkt(2, false))
	o.Observe(engine.C2S, dataPkt(2, true)) // retransmit of seq 2
	o.Observe(engine.S2C, ctrl(CtrlHandshake))
	o.Observe(engine.S2C, ctrl(CtrlKeepalive))
	o.Observe(engine.S2C, ctrl(CtrlShutdown))
	o.Observe(engine.S2C, ctrl(CtrlACKACK))
	o.Observe(engine.S2C, ctrl(CtrlACK, 5))
	o.Observe(engine.S2C, ctrl(CtrlNAK, 7, 0x80000000|10, 12))

	obs := o.Observation()
	if obs.DataPackets != 3 {
		t.Errorf("DataPackets = %d, want 3", obs.DataPackets)
	}
	if obs.ControlPackets != 6 {
		t.Errorf("ControlPackets = %d, want 6", obs.ControlPackets)
	}
	if obs.Handshakes != 1 || obs.KeepAlives != 1 || obs.Shutdowns != 1 || obs.AckAcks != 1 {
		t.Errorf("ctrl mix off: %+v", obs)
	}
	if obs.ACKs != 1 || obs.NAKs != 1 {
		t.Errorf("ACKs=%d NAKs=%d, want 1/1", obs.ACKs, obs.NAKs)
	}
	if obs.Retransmitted != 1 {
		t.Errorf("Retransmitted = %d, want 1", obs.Retransmitted)
	}
	if !obs.RetransSeqs[2] {
		t.Errorf("RetransSeqs missing seq 2: %v", obs.RetransSeqs)
	}
	for _, s := range []uint32{7, 10, 11, 12} {
		if !obs.NakedSeqs[s] {
			t.Errorf("NakedSeqs missing %d: %v", s, obs.NakedSeqs)
		}
	}
	if obs.MaxAckSeq != 5 {
		t.Errorf("MaxAckSeq = %d, want 5", obs.MaxAckSeq)
	}
	if !obs.AckMonotonic {
		t.Error("AckMonotonic = false, want true")
	}
}

func TestObserverAckMonotonicViolation(t *testing.T) {
	o := NewObserver()
	o.Observe(engine.S2C, ctrl(CtrlACK, 100))
	o.Observe(engine.S2C, ctrl(CtrlACK, 150))
	if !o.Observation().AckMonotonic {
		t.Fatal("AckMonotonic went false on increasing ACKs")
	}
	o.Observe(engine.S2C, ctrl(CtrlACK, 120)) // decreasing -> violation
	obs := o.Observation()
	if obs.AckMonotonic {
		t.Error("AckMonotonic = true after decreasing ACK, want false")
	}
	if obs.MaxAckSeq != 150 {
		t.Errorf("MaxAckSeq = %d, want 150 (max preserved)", obs.MaxAckSeq)
	}
}

func TestObserverEqualAckOK(t *testing.T) {
	// A repeated (equal) ACK is not a backwards move.
	o := NewObserver()
	o.Observe(engine.S2C, ctrl(CtrlACK, 42))
	o.Observe(engine.S2C, ctrl(CtrlACK, 42))
	if !o.Observation().AckMonotonic {
		t.Error("AckMonotonic = false on repeated equal ACK")
	}
}

func TestObserverIgnoresShortDatagrams(t *testing.T) {
	o := NewObserver()
	o.Observe(engine.C2S, []byte{1, 2, 3})
	obs := o.Observation()
	if obs.DataPackets != 0 || obs.ControlPackets != 0 {
		t.Errorf("short datagram counted: %+v", obs)
	}
}

// TestObserverRealCaptureMix replays a control/data mix shaped like a real
// srtgo loopback capture (handshakes, data, ACK/ACKACK/keepalive, plus a
// NAK and the retransmit it provokes) and sanity-checks the tallies. The byte
// patterns (e.g. data w1=0xE0000001 vs retransmit 0xE4000003, NAK CIF carrying
// the lost seq which then reappears as a retransmitted DATA) were validated
// against an actual srtgo capture.
func TestObserverRealCaptureMix(t *testing.T) {
	o := NewObserver()
	const lost = 441826218 // 0x1A55BBAA, real observed lost/retransmitted seq

	// HSv5 induction + conclusion handshakes both ways.
	o.Observe(engine.C2S, ctrl(CtrlHandshake))
	o.Observe(engine.S2C, ctrl(CtrlHandshake))
	o.Observe(engine.C2S, ctrl(CtrlHandshake))
	o.Observe(engine.S2C, ctrl(CtrlHandshake))

	// Original data flow.
	for seq := uint32(lost - 2); seq <= lost+2; seq++ {
		o.Observe(engine.C2S, dataPkt(seq, false))
	}
	// Receiver ACKs progress, sender ACKACKs.
	o.Observe(engine.S2C, ctrl(CtrlACK, lost-1))
	o.Observe(engine.C2S, ctrl(CtrlACKACK))
	// Loss report for the dropped packet, then its retransmission.
	o.Observe(engine.S2C, ctrl(CtrlNAK, lost))
	o.Observe(engine.C2S, dataPkt(lost, true))
	// Keepalive and final ACK covering everything.
	o.Observe(engine.S2C, ctrl(CtrlKeepalive))
	o.Observe(engine.S2C, ctrl(CtrlACK, lost+3))

	obs := o.Observation()
	if obs.Handshakes != 4 {
		t.Errorf("Handshakes = %d, want 4", obs.Handshakes)
	}
	if obs.DataPackets != 6 { // 5 originals + 1 retransmit
		t.Errorf("DataPackets = %d, want 6", obs.DataPackets)
	}
	if obs.Retransmitted != 1 || !obs.RetransSeqs[lost] {
		t.Errorf("retransmit tracking off: retrans=%d seqs=%v", obs.Retransmitted, obs.RetransSeqs)
	}
	if obs.NAKs != 1 || !obs.NakedSeqs[lost] {
		t.Errorf("NAK tracking off: naks=%d seqs=%v", obs.NAKs, obs.NakedSeqs)
	}
	if obs.ACKs != 2 || obs.AckAcks != 1 || obs.KeepAlives != 1 {
		t.Errorf("control mix off: %+v", obs)
	}
	if !obs.AckMonotonic || obs.MaxAckSeq != lost+3 {
		t.Errorf("ACK progression off: mono=%v max=%d", obs.AckMonotonic, obs.MaxAckSeq)
	}
}
