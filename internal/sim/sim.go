// Package sim is the deterministic driver for the Sans-I/O engine. It feeds a
// reproducible packet trace through an Engine in virtual time and records the
// realized schedule (the "pattern") as a stable, diffable artifact. This is how
// P0 proves determinism: same seed + same trace -> byte-identical pattern, on
// any machine. No sockets, no wall clock.
package sim

import (
	"fmt"
	"strings"

	"github.com/zsiec/impair/internal/engine"
)

// Event is one ingress packet in a synthetic trace.
type Event struct {
	At  int64 // ns, virtual ingress time
	Dir engine.Direction
	Len int
}

// SyntheticTrace generates n packets spaced by interval ns, alternating
// direction, with deterministic content. It is a stand-in workload for
// determinism testing until real protocol traces are wired in (P1).
func SyntheticTrace(n int, interval int64) []Event {
	ev := make([]Event, n)
	for i := range ev {
		dir := engine.C2S
		if i%4 == 3 { // ~1 in 4 packets is the return path
			dir = engine.S2C
		}
		ev[i] = Event{At: int64(i) * interval, Dir: dir, Len: 1316}
	}
	return ev
}

// RunActions feeds the trace through eng and invokes fn(recvAt, action) for
// every resulting Action, in ingress order. It is the shared primitive behind
// Run, RunStats, and pattern recording (fn can be a *pattern.Recorder's Add).
// Packet content is deterministic (byte(i+j)) so the whole run is reproducible.
func RunActions(eng *engine.Engine, trace []Event, fn func(recvAt int64, a engine.Action)) {
	for i, ev := range trace {
		data := make([]byte, ev.Len)
		for j := range data {
			data[j] = byte(i + j)
		}
		for _, a := range eng.Handle(engine.Packet{Data: data, Dir: ev.Dir}, ev.At) {
			fn(ev.At, a)
		}
	}
}

// Run feeds the trace through eng and returns the action log as a stable string,
// keyed by ingress order (deterministic regardless of delivery reordering). The
// canonical golden artifact uses the pattern package; this remains a quick
// human-readable dump.
func Run(eng *engine.Engine, trace []Event) string {
	var b strings.Builder
	RunActions(eng, trace, func(recvAt int64, a engine.Action) {
		if a.Kind == engine.Drop {
			fmt.Fprintf(&b, "t=%d %s DROP seq=%d by=%s\n", recvAt, a.Dir, a.Seq, a.Reason)
		} else {
			fmt.Fprintf(&b, "t=%d %s FWD  seq=%d deliver=%d len=%d\n", recvAt, a.Dir, a.Seq, a.DeliverAt, len(a.Data))
		}
	})
	return b.String()
}

// Stats summarizes a run for quick sanity checks.
type Stats struct {
	Ingress   int
	Forwarded int
	Dropped   int
	Reordered int // forwards whose DeliverAt precedes a prior forward's DeliverAt (same dir)
}

// RunStats is like Run but returns aggregate counters instead of the full log.
func RunStats(eng *engine.Engine, trace []Event) Stats {
	s := Stats{Ingress: len(trace)}
	var lastDeliver [2]int64
	var haveLast [2]bool
	RunActions(eng, trace, func(recvAt int64, a engine.Action) {
		switch a.Kind {
		case engine.Drop:
			s.Dropped++
		case engine.Forward:
			s.Forwarded++
			if haveLast[a.Dir] && a.DeliverAt < lastDeliver[a.Dir] {
				s.Reordered++
			}
			lastDeliver[a.Dir] = a.DeliverAt
			haveLast[a.Dir] = true
		}
	})
	return s
}
