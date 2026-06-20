// Package engine is the Sans-I/O core of Impair. It is the deterministic
// decision layer: given a packet and a virtual ingress time, it runs the packet
// through an ordered per-direction pipeline of impairment Cells and returns the
// resulting Actions (forward-at-a-time, or drop-with-reason). It performs no I/O
// and reads no real clock, so a given (seed, cell config, input trace) produces
// a byte-identical Action stream every run — on any machine. A driver (the
// deterministic sim driver, or a real UDP relay) is responsible for actually
// delivering Forward actions at their DeliverAt.
package engine

import "fmt"

// Direction identifies which way a packet is travelling through the relay.
type Direction uint8

const (
	// C2S is client-to-server (e.g. SRT caller -> listener).
	C2S Direction = iota
	// S2C is server-to-client (the return path: ACK/NAK/replies).
	S2C
	numDir
)

func (d Direction) String() string {
	switch d {
	case C2S:
		return "c2s"
	case S2C:
		return "s2c"
	default:
		return "?"
	}
}

// Packet is one datagram entering the engine.
type Packet struct {
	Data []byte
	Dir  Direction
}

// InFlight is a packet moving through the cell pipeline. Cells may mutate Data
// (corruption) and DeliverAt (delay/jitter/reorder), return zero outputs (drop),
// or return more than one (duplication). Seq is a monotonic ingress id assigned
// by the engine; duplicates produced by a cell share their parent's Seq.
type InFlight struct {
	Seq       uint64
	Dir       Direction
	Data      []byte
	RecvAt    int64 // ns, virtual ingress time (immutable)
	DeliverAt int64 // ns, when the driver should forward it (>= RecvAt)
}

// ActionKind is the disposition of a packet after the pipeline.
type ActionKind uint8

const (
	// Forward = deliver Data on Dir at DeliverAt.
	Forward ActionKind = iota
	// Drop = the packet was dropped by the cell named in Reason.
	Drop
)

// Action is an instruction the driver must carry out.
type Action struct {
	Kind      ActionKind
	Dir       Direction
	Seq       uint64
	Data      []byte
	DeliverAt int64
	Reason    string // drop cause (cell name) for Drop; "" for Forward
}

// Cell is one stage of an impairment pipeline. Implementations are stateful and
// hold their own rng.Source substream; they MUST be deterministic functions of
// their state + that substream (no real time, no global rng, no maps-iteration
// nondeterminism). Returning an empty slice drops the packet (the engine records
// the cell's Name as the reason).
type Cell interface {
	Name() string
	Process(in InFlight) []InFlight
}

// Engine runs one ordered Cell pipeline per direction.
type Engine struct {
	pipelines [numDir][]Cell
	seq       uint64
}

// New builds an Engine from per-direction pipelines (cells applied in order).
func New(c2s, s2c []Cell) *Engine {
	e := &Engine{}
	e.pipelines[C2S] = c2s
	e.pipelines[S2C] = s2c
	return e
}

// Handle pushes one ingress packet through its direction's pipeline at recvAt
// (ns, virtual) and returns the resulting Actions in a deterministic order:
// drops in the order cells dropped them, then surviving forwards. DeliverAt on a
// Forward may precede that of an earlier-ingress packet (reordering); ordering
// of delivery is the driver's concern, not the engine's.
func (e *Engine) Handle(p Packet, recvAt int64) []Action {
	if p.Dir >= numDir {
		panic(fmt.Sprintf("engine: invalid direction %d", p.Dir))
	}
	e.seq++
	cur := []InFlight{{Seq: e.seq, Dir: p.Dir, Data: p.Data, RecvAt: recvAt, DeliverAt: recvAt}}
	var actions []Action
	for _, cell := range e.pipelines[p.Dir] {
		var next []InFlight
		for _, f := range cur {
			outs := cell.Process(f)
			if len(outs) == 0 {
				actions = append(actions, Action{Kind: Drop, Dir: f.Dir, Seq: f.Seq, Reason: cell.Name()})
				continue
			}
			next = append(next, outs...)
		}
		cur = next
	}
	for _, f := range cur {
		actions = append(actions, Action{Kind: Forward, Dir: f.Dir, Seq: f.Seq, Data: f.Data, DeliverAt: f.DeliverAt})
	}
	return actions
}
