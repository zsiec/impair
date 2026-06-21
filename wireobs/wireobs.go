// Package wireobs is the protocol-agnostic spine shared by the per-protocol wire
// observers (SRT `wire`, RIST `ristwire`, and any future ones). Counters holds
// the facts every reliable media transport produces — a data/control packet
// split, retransmit requests and the sequence numbers they name, and the
// retransmissions seen on the wire — with the fold bookkeeping that turns a
// decoded packet into a tally. Each protocol's Observation embeds Counters and
// adds only its own control-plane vocabulary; each protocol's Observer keeps its
// own wire decoder and control switch. This is what lets a new protocol's
// observer reuse the spine instead of re-deriving it.
package wireobs

// Counters is the shared wire-fact spine, generic over the protocol's sequence
// number width (SRT uses uint32, RIST's RTP uses uint16).
type Counters[S comparable] struct {
	DataPackets    int        // media/data-plane packets (SRT DATA, RIST RTP)
	ControlPackets int        // control-plane packets (SRT control, RIST RTCP)
	RetransReqs    int        // retransmit requests observed (SRT NAKs, RIST NACKs)
	Retransmitted  int        // packets seen retransmitted on the wire
	ReqSeqs        map[S]bool // sequence numbers named in a retransmit request
	RetransSeqs    map[S]bool // sequence numbers seen retransmitted
}

// NewCounters returns a Counters with its sets initialized.
func NewCounters[S comparable]() Counters[S] {
	return Counters[S]{
		ReqSeqs:     make(map[S]bool),
		RetransSeqs: make(map[S]bool),
	}
}

// Data records one data-plane packet.
func (c *Counters[S]) Data() { c.DataPackets++ }

// Control records one control-plane packet.
func (c *Counters[S]) Control() { c.ControlPackets++ }

// Request records a retransmit request naming the given (already range-expanded)
// sequence numbers.
func (c *Counters[S]) Request(seqs []S) {
	c.RetransReqs++
	for _, s := range seqs {
		c.ReqSeqs[s] = true
	}
}

// Retransmit records one packet seen retransmitted on the wire.
func (c *Counters[S]) Retransmit(s S) {
	c.Retransmitted++
	c.RetransSeqs[s] = true
}
