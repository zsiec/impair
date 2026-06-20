package ristwire

import "testing"

func TestObserverCounts(t *testing.T) {
	o := NewObserver()
	o.Observe(rtpPacket(96, 1, 0, 0xABCD))
	o.Observe(rtpPacket(96, 2, 0, 0xABCD))
	o.Observe(rtcpSR(0xABCD))
	o.Observe(rtcpRR(0xABCD))
	o.Observe(rtcpRR(0xABCD))

	obs := o.Observation()
	if obs.RTPPackets != 2 {
		t.Errorf("RTPPackets = %d, want 2", obs.RTPPackets)
	}
	if obs.RTCPPackets != 3 {
		t.Errorf("RTCPPackets = %d, want 3", obs.RTCPPackets)
	}
	if obs.SenderReports != 1 {
		t.Errorf("SenderReports = %d, want 1", obs.SenderReports)
	}
	if obs.ReceiverReports != 2 {
		t.Errorf("ReceiverReports = %d, want 2", obs.ReceiverReports)
	}
	if !obs.SSRCs[0xABCD] {
		t.Error("SSRC 0xABCD not recorded")
	}
}

// TestObserverRetransmit: a repeated RTP sequence number is a retransmission.
func TestObserverRetransmit(t *testing.T) {
	o := NewObserver()
	o.Observe(rtpPacket(96, 100, 0, 1))
	o.Observe(rtpPacket(96, 101, 0, 1))
	o.Observe(rtpPacket(96, 100, 0, 1)) // retransmit of 100

	obs := o.Observation()
	if obs.RTPPackets != 3 {
		t.Errorf("RTPPackets = %d, want 3", obs.RTPPackets)
	}
	if obs.Retransmitted != 1 {
		t.Errorf("Retransmitted = %d, want 1", obs.Retransmitted)
	}
	if !obs.RetransSeqs[100] {
		t.Error("seq 100 not flagged as retransmitted")
	}
	// A retransmit must not be counted as a gap or move MaxSeq.
	if obs.SeqGaps != 0 {
		t.Errorf("SeqGaps = %d, want 0", obs.SeqGaps)
	}
	if obs.MaxSeq != 101 {
		t.Errorf("MaxSeq = %d, want 101", obs.MaxSeq)
	}
}

// TestObserverGaps: skipped sequence numbers in the forward progression count
// as gaps.
func TestObserverGaps(t *testing.T) {
	o := NewObserver()
	o.Observe(rtpPacket(96, 10, 0, 1))
	o.Observe(rtpPacket(96, 11, 0, 1))
	o.Observe(rtpPacket(96, 15, 0, 1)) // skips 12,13,14 -> 3 gaps
	o.Observe(rtpPacket(96, 16, 0, 1))

	obs := o.Observation()
	if obs.SeqGaps != 3 {
		t.Errorf("SeqGaps = %d, want 3", obs.SeqGaps)
	}
	if obs.MaxSeq != 16 {
		t.Errorf("MaxSeq = %d, want 16", obs.MaxSeq)
	}
}

// TestObserverGapWrap: the gap counter handles a 16-bit wrap-around.
func TestObserverGapWrap(t *testing.T) {
	o := NewObserver()
	o.Observe(rtpPacket(96, 0xFFFE, 0, 1))
	o.Observe(rtpPacket(96, 0xFFFF, 0, 1))
	o.Observe(rtpPacket(96, 2, 0, 1)) // wraps past 0,1 -> skips 0,1 = 2 gaps

	obs := o.Observation()
	if obs.SeqGaps != 2 {
		t.Errorf("SeqGaps = %d, want 2", obs.SeqGaps)
	}
	if obs.MaxSeq != 2 {
		t.Errorf("MaxSeq = %d, want 2", obs.MaxSeq)
	}
}

// TestObserverReorderNoGap: an out-of-order (backward) sequence number is not a
// gap and does not pull MaxSeq backwards.
func TestObserverReorderNoGap(t *testing.T) {
	o := NewObserver()
	o.Observe(rtpPacket(96, 100, 0, 1))
	o.Observe(rtpPacket(96, 105, 0, 1)) // 4 gaps
	o.Observe(rtpPacket(96, 102, 0, 1)) // reordered, fills in; not a new gap

	obs := o.Observation()
	if obs.SeqGaps != 4 {
		t.Errorf("SeqGaps = %d, want 4", obs.SeqGaps)
	}
	if obs.MaxSeq != 105 {
		t.Errorf("MaxSeq = %d, want 105", obs.MaxSeq)
	}
}

// TestObserverNACK: an RTPFB NACK is counted and its requested seqs collected.
func TestObserverNACK(t *testing.T) {
	o := NewObserver()
	o.Observe(rtcpNACK(1, 2, [2]uint16{500, 0b101})) // 500, 501, 503

	obs := o.Observation()
	if obs.NACKs != 1 {
		t.Errorf("NACKs = %d, want 1", obs.NACKs)
	}
	if obs.RTCPPackets != 1 {
		t.Errorf("RTCPPackets = %d, want 1", obs.RTCPPackets)
	}
	for _, s := range []uint16{500, 501, 503} {
		if !obs.NackedSeqs[s] {
			t.Errorf("seq %d not in NackedSeqs", s)
		}
	}
}

// TestObserverFullFlow exercises the whole pipeline as the relay sees it: the
// sender's original RTP (with one datagram dropped on the wire, so the relay
// observes a gap), a receiver report, a NACK requesting the lost seq, and the
// retransmit. The retransmit is the wire signature of a sequence number seen a
// second time — so seq 3 is observed once before the loss-region (it was sent),
// then again as the retransmit. The relay sees: 1,2,3(orig but its successor 4
// is dropped),5 ... then the retransmit of 4.
func TestObserverFullFlow(t *testing.T) {
	o := NewObserver()
	o.Observe(rtpPacket(96, 1, 0, 7))
	o.Observe(rtpPacket(96, 2, 0, 7))
	o.Observe(rtpPacket(96, 3, 0, 7))
	o.Observe(rtpPacket(96, 5, 0, 7))          // 4 lost -> 1 gap
	o.Observe(rtcpRR(7))                       // receiver report
	o.Observe(rtcpNACK(9, 7, [2]uint16{4, 0})) // request seq 4
	o.Observe(rtpPacket(96, 4, 0, 7))          // (re)appearance of 4 fills the gap
	o.Observe(rtpPacket(96, 5, 0, 7))          // retransmit of 5 (seen twice)

	obs := o.Observation()
	if obs.RTPPackets != 6 {
		t.Errorf("RTPPackets = %d, want 6", obs.RTPPackets)
	}
	if obs.SeqGaps != 1 {
		t.Errorf("SeqGaps = %d, want 1", obs.SeqGaps)
	}
	if obs.NACKs != 1 {
		t.Errorf("NACKs = %d, want 1", obs.NACKs)
	}
	if !obs.NackedSeqs[4] {
		t.Error("seq 4 not in NackedSeqs")
	}
	if obs.Retransmitted != 1 || !obs.RetransSeqs[5] {
		t.Errorf("retransmit of seq 5 not detected: Retransmitted=%d", obs.Retransmitted)
	}
}
