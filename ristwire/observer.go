package ristwire

// Observer accumulates RIST wire facts across a run. It decodes each datagram
// passed to Observe and tallies the RTP/RTCP mix, per-RTCP-type counts (SR/RR),
// retransmit requests (RTPFB NACKs) and the sequence numbers they request,
// retransmissions (an RTP sequence number observed more than once), and the
// gaps in the RTP sequence progression. The resulting Observation is what the
// RIST oracle judges against expected protocol behaviour.
type Observer struct {
	obs Observation
	// seen tracks every RTP sequence number observed, so a repeat is identified
	// as a retransmission. RIST retransmits carry the original RTP header (same
	// sequence number), so a second sighting of a sequence number is the wire
	// signature of a retransmit.
	seen map[uint16]bool
}

// NewObserver returns an Observer ready to accumulate.
func NewObserver() *Observer {
	return &Observer{
		obs: Observation{
			NackedSeqs:  make(map[uint16]bool),
			RetransSeqs: make(map[uint16]bool),
			SSRCs:       make(map[uint32]bool),
		},
		seen: make(map[uint16]bool),
	}
}

// Observe decodes one datagram and folds its facts into the running tally.
// Datagrams too short to decode are ignored. The direction is irrelevant to
// the RIST observation (both directions feed the same Observation), so Observe
// takes only the datagram.
func (o *Observer) Observe(data []byte) {
	p, ok := Decode(data)
	if !ok {
		return
	}

	o.obs.SSRCs[p.SSRC] = true

	if p.IsRTCP {
		o.obs.RTCPPackets++
		switch p.PayloadType {
		case RTCPSenderReport:
			o.obs.SenderReports++
		case RTCPReceiverReport:
			o.obs.ReceiverReports++
		case RTCPRTPFB:
			// A transport-layer feedback packet is RIST's retransmit request.
			o.obs.NACKs++
			for _, s := range p.NackSeqs {
				o.obs.NackedSeqs[s] = true
			}
		}
		return
	}

	// RTP.
	o.obs.RTPPackets++
	o.recordSeq(p.Seq)
}

// recordSeq folds one RTP sequence number into the tally: it detects a
// retransmission (a sequence number seen before) and counts the gaps opened in
// the forward sequence progression (handling 16-bit wrap).
func (o *Observer) recordSeq(seq uint16) {
	if o.seen[seq] {
		o.obs.Retransmitted++
		o.obs.RetransSeqs[seq] = true
		return
	}
	o.seen[seq] = true

	if !o.obs.SeqSeen {
		o.obs.SeqSeen = true
		o.obs.MaxSeq = seq
		return
	}

	// Count gaps only on forward progression. A forward step is a positive
	// 16-bit distance in the lower half of the sequence space (<= 0x8000); any
	// larger distance is treated as a (reordered/old) backward step and opens no
	// gap. The number of skipped sequence numbers is the forward distance minus
	// one.
	delta := seq - o.obs.MaxSeq // uint16 wrap-around arithmetic
	if delta != 0 && delta <= 0x8000 {
		o.obs.SeqGaps += int(delta) - 1
		o.obs.MaxSeq = seq
	}
}

// Observation returns the accumulated RIST wire facts.
func (o *Observer) Observation() Observation {
	return o.obs
}
