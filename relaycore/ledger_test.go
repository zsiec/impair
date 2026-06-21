package relaycore

import (
	"testing"

	"github.com/zsiec/impair/engine"
)

// The ring is bounded and overwrite-oldest: more records than capacity keep only
// the most-recent cap entries, in chronological order, oldest first.
func TestLedgerRingBounded(t *testing.T) {
	l := newLedger(3)
	for i := 1; i <= 7; i++ { // 7 records into a cap-3 ring
		h := l.start(uint64(i), engine.C2S, int64(i*10), int64(i*10+1))
		l.finalize(h, int64(i*10+2))
	}
	snap := l.snapshot()
	if len(snap) != 3 {
		t.Fatalf("snapshot len = %d, want 3 (bounded by cap)", len(snap))
	}
	// Only seqs 5,6,7 survive, oldest first.
	for i, want := range []uint64{5, 6, 7} {
		if snap[i].Seq != want {
			t.Fatalf("snap[%d].Seq = %d, want %d", i, snap[i].Seq, want)
		}
		if snap[i].EgressAt == 0 {
			t.Fatalf("snap[%d] not finalized", i)
		}
	}
}

// Before the ring wraps, the snapshot is the records in insertion order.
func TestLedgerSnapshotOrderUnwrapped(t *testing.T) {
	l := newLedger(8)
	for i := 1; i <= 4; i++ {
		h := l.start(uint64(i), engine.C2S, int64(i), int64(i))
		l.finalize(h, int64(i))
	}
	snap := l.snapshot()
	if len(snap) != 4 {
		t.Fatalf("len = %d, want 4", len(snap))
	}
	for i, want := range []uint64{1, 2, 3, 4} {
		if snap[i].Seq != want {
			t.Fatalf("snap[%d].Seq = %d, want %d", i, snap[i].Seq, want)
		}
	}
}

// A finalize against a slot that has since been reused (generation mismatch) is
// a no-op — it must never corrupt the entry that now occupies the slot.
func TestLedgerFinalizeStaleHandle(t *testing.T) {
	l := newLedger(1) // cap 1: every start reuses slot 0
	h1 := l.start(1, engine.C2S, 10, 20)
	h2 := l.start(2, engine.C2S, 30, 40) // overwrites slot 0, bumps generation
	l.finalize(h1, 999)                  // stale: must be ignored
	l.finalize(h2, 50)
	snap := l.snapshot()
	if len(snap) != 1 {
		t.Fatalf("len = %d, want 1", len(snap))
	}
	if snap[0].Seq != 2 || snap[0].EgressAt != 50 {
		t.Fatalf("stale finalize corrupted entry: %+v", snap[0])
	}
}
