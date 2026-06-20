package wire

import "github.com/zsiec/impair/engine"

// Observer accumulates SRT wire facts across a run. It decodes each datagram
// passed to Observe (in either direction) and tallies the control/data mix,
// retransmissions, NAK'd sequence numbers, and ACK progression. The resulting
// Observation is what the oracle judges against expected protocol behaviour.
type Observer struct {
	obs Observation
	// ACK monotonicity is tracked PER DIRECTION: SRT is bidirectional, so each
	// endpoint's receiver-side emits its own ACK stream (the data receiver's
	// ACKs advance; an idle endpoint's ACKs sit at its ISN). Pooling the two
	// would spuriously look non-monotonic.
	ackSeen [2]bool
	lastAck [2]uint32
}

// NewObserver returns an Observer ready to accumulate. AckMonotonic starts true
// and only goes false if a later ACK acknowledges a lower sequence number.
func NewObserver() *Observer {
	return &Observer{
		obs: Observation{
			NakedSeqs:    make(map[uint32]bool),
			RetransSeqs:  make(map[uint32]bool),
			AckMonotonic: true,
		},
	}
}

// Observe decodes one datagram and folds its facts into the running tally.
// Datagrams too short to decode are ignored. The direction is currently
// informational (both directions feed the same Observation).
func (o *Observer) Observe(dir engine.Direction, data []byte) {
	p, ok := Decode(data)
	if !ok {
		return
	}

	if !p.IsControl {
		o.obs.DataPackets++
		if p.Retrans {
			o.obs.Retransmitted++
			o.obs.RetransSeqs[p.Seq] = true
		}
		return
	}

	o.obs.ControlPackets++
	switch p.ControlType {
	case CtrlHandshake:
		o.obs.Handshakes++
	case CtrlKeepalive:
		o.obs.KeepAlives++
	case CtrlShutdown:
		o.obs.Shutdowns++
	case CtrlACKACK:
		o.obs.AckAcks++
	case CtrlACK:
		o.obs.ACKs++
		o.recordAck(dir, p.AckSeq)
	case CtrlNAK:
		o.obs.NAKs++
		for _, s := range p.NakSeqs {
			o.obs.NakedSeqs[s] = true
		}
	}
}

// recordAck tracks the highest acknowledged sequence number and detects any
// backwards movement in the ACK'd sequence.
func (o *Observer) recordAck(dir engine.Direction, seq uint32) {
	d := int(dir)
	if o.ackSeen[d] && seq < o.lastAck[d] {
		o.obs.AckMonotonic = false
	}
	if seq > o.obs.MaxAckSeq {
		o.obs.MaxAckSeq = seq
	}
	o.lastAck[d] = seq
	o.ackSeen[d] = true
}

// Observation returns the accumulated wire facts.
func (o *Observer) Observation() Observation {
	return o.obs
}
