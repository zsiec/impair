package wire

import "github.com/zsiec/impair/engine"

// Observer accumulates SRT wire facts across a run. It decodes each datagram
// passed to Observe (in either direction) and tallies the control/data mix,
// retransmissions, NAK'd sequence numbers, and ACK progression. The resulting
// Observation is what the oracle judges against expected protocol behaviour.
type Observer struct {
	obs     Observation
	ackSeen bool   // whether any ACK seq has been observed yet
	lastAck uint32 // most recently observed ACK'd sequence number
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
	_ = dir
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
		o.recordAck(p.AckSeq)
	case CtrlNAK:
		o.obs.NAKs++
		for _, s := range p.NakSeqs {
			o.obs.NakedSeqs[s] = true
		}
	}
}

// recordAck tracks the highest acknowledged sequence number and detects any
// backwards movement in the ACK'd sequence.
func (o *Observer) recordAck(seq uint32) {
	if o.ackSeen && seq < o.lastAck {
		o.obs.AckMonotonic = false
	}
	if !o.ackSeen || seq > o.obs.MaxAckSeq {
		o.obs.MaxAckSeq = seq
	}
	o.lastAck = seq
	o.ackSeen = true
}

// Observation returns the accumulated wire facts.
func (o *Observer) Observation() Observation {
	return o.obs
}
