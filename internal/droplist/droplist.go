// Package droplist provides two deterministic, data-driven impairment Cells:
//
//   - DropList replicates ns-3's ListErrorModel: it drops every packet whose
//     monotonic ingress Seq appears in an explicit set ("drop #2,#3,#7") and
//     passes everything else untouched.
//
//   - DeliveryTrace replicates a Mahimahi-style delivery-opportunity schedule.
//     A trace is a sorted list of millisecond timestamps; each timestamp grants
//     one MTU of delivery capacity. Packets are paced to the next free
//     opportunity (DeliverAt is set accordingly) and dropped when the backlog of
//     bytes waiting for an opportunity exceeds MaxBacklog.
//
// Neither model needs randomness; both accept an *rng.Source for API uniformity
// with the other cells. Both are fully deterministic functions of their config
// and the ingress packet stream.
package droplist

import (
	"bufio"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/zsiec/impair/internal/engine"
	"github.com/zsiec/impair/internal/rng"
)

// ---------------------------------------------------------------------------
// DropList — ns-3 ListErrorModel.
// ---------------------------------------------------------------------------

// DropListConfig configures a DropList cell.
//
// The zero value drops nothing (an empty Seqs set passes every packet).
type DropListConfig struct {
	// Seqs is the explicit set of ingress Seq values to drop. The engine
	// assigns Seq starting at 1 for the first packet it sees (see
	// engine.Engine.Handle), so to drop "the 2nd, 3rd and 7th packet" use
	// {2, 3, 7}.
	Seqs []uint64
}

// dropList is the ns-3 ListErrorModel: drop packets whose ingress Seq is in the
// configured set, pass the rest.
type dropList struct {
	drop map[uint64]struct{}
}

// NewDropList builds a DropList cell. src is accepted for API uniformity and is
// unused (the model is purely data-driven and deterministic).
func NewDropList(cfg DropListConfig, src *rng.Source) engine.Cell {
	_ = src
	d := &dropList{drop: make(map[uint64]struct{}, len(cfg.Seqs))}
	for _, s := range cfg.Seqs {
		d.drop[s] = struct{}{}
	}
	return d
}

func (d *dropList) Name() string { return "droplist" }

func (d *dropList) Process(in engine.InFlight) []engine.InFlight {
	if _, ok := d.drop[in.Seq]; ok {
		return nil // drop
	}
	return []engine.InFlight{in}
}

// ---------------------------------------------------------------------------
// DeliveryTrace — Mahimahi-style delivery-opportunity schedule.
// ---------------------------------------------------------------------------

// DeliveryTraceConfig configures a DeliveryTrace cell.
type DeliveryTraceConfig struct {
	// Schedule is a list of delivery-opportunity timestamps in milliseconds,
	// relative to the first packet's ingress time (RecvAt of the first packet
	// seen == trace time 0). It need not be pre-sorted; New sorts a copy. Each
	// opportunity grants MTU bytes of delivery capacity.
	//
	// Mahimahi traces typically loop. Loop controls that; see below.
	Schedule []int64

	// MTU is the number of bytes delivered per opportunity (e.g. 1500). A
	// packet larger than MTU consumes as many consecutive opportunities as
	// needed (ceil(len/MTU)). Must be > 0; if 0, MTU defaults to 1500.
	MTU int

	// MaxBacklog bounds the number of bytes allowed to be queued waiting for a
	// future delivery opportunity. When admitting a packet would push the
	// queued backlog (bytes whose opportunity lies in the future relative to
	// the packet's own RecvAt) past MaxBacklog, the packet is dropped. A
	// MaxBacklog of 0 means unbounded (never drop for backlog).
	MaxBacklog int

	// Loop, when true, repeats the schedule indefinitely: after the last
	// timestamp T (with N entries), opportunity index i (i>=N) occurs at
	// T_period*floor(i/N) + Schedule[i%N], where T_period is the trace
	// duration (last timestamp). This matches Mahimahi's cyclic replay. When
	// false, opportunities past the end of the schedule do not exist and any
	// packet needing one is dropped (treated as backlog overflow).
	Loop bool
}

const defaultMTU = 1500

// deliveryTrace paces packets onto a fixed schedule of delivery opportunities.
//
// State: nextOpp is the index of the next unused opportunity; base is the
// virtual ingress time (ns) that maps to trace time 0 (set from the first
// packet's RecvAt).
type deliveryTrace struct {
	sched      []int64 // sorted, ms, copy of config
	periodMs   int64   // last timestamp; only meaningful when loop
	mtu        int
	maxBacklog int
	loop       bool

	started bool
	baseNs  int64 // RecvAt of first packet => trace time 0
	nextOpp int   // index of next free opportunity
}

// NewDeliveryTrace builds a DeliveryTrace cell. src is accepted for API
// uniformity and is unused.
func NewDeliveryTrace(cfg DeliveryTraceConfig, src *rng.Source) engine.Cell {
	_ = src
	mtu := cfg.MTU
	if mtu <= 0 {
		mtu = defaultMTU
	}
	sched := make([]int64, len(cfg.Schedule))
	copy(sched, cfg.Schedule)
	sort.Slice(sched, func(i, j int) bool { return sched[i] < sched[j] })
	var period int64
	if len(sched) > 0 {
		period = sched[len(sched)-1]
	}
	return &deliveryTrace{
		sched:      sched,
		periodMs:   period,
		mtu:        mtu,
		maxBacklog: cfg.MaxBacklog,
		loop:       cfg.Loop,
	}
}

func (t *deliveryTrace) Name() string { return "deliverytrace" }

// oppTimeMs returns the trace-relative time (ms) of opportunity index i, and
// whether that opportunity exists. Non-looping traces have only len(sched)
// opportunities.
func (t *deliveryTrace) oppTimeMs(i int) (int64, bool) {
	n := len(t.sched)
	if n == 0 {
		return 0, false
	}
	if i < n {
		return t.sched[i], true
	}
	if !t.loop {
		return 0, false
	}
	cycle := int64(i / n)
	idx := i % n
	return cycle*t.periodMs + t.sched[idx], true
}

func (t *deliveryTrace) Process(in engine.InFlight) []engine.InFlight {
	if len(t.sched) == 0 {
		// No opportunities at all: nothing can ever be delivered.
		return nil
	}
	if !t.started {
		t.started = true
		t.baseNs = in.RecvAt
	}

	// Number of opportunities this packet consumes.
	need := (len(in.Data) + t.mtu - 1) / t.mtu
	if need < 1 {
		need = 1 // a zero-length packet still consumes one opportunity
	}

	// Trace-relative arrival time (ms).
	arrMs := nsToMsFloor(in.RecvAt - t.baseNs)

	// arrIdx is the index of the first opportunity occurring at-or-after this
	// packet's arrival, independent of how many opportunities prior packets
	// have already consumed. It is the reference point for the backlog: any
	// opportunity at index >= arrIdx that is already claimed represents queued
	// (backlogged) bytes waiting for the link.
	arrIdx := t.firstOppAtOrAfter(arrMs)
	if arrIdx < 0 {
		return nil // schedule exhausted (non-looping) before this packet arrives
	}

	// The packet starts service at the first free opportunity at-or-after both
	// its arrival and the cursor of already-consumed opportunities.
	startIdx := t.nextOpp
	if arrIdx > startIdx {
		// Link was idle while waiting for this packet: skip the gap.
		startIdx = arrIdx
	}

	// The packet's delivering (last) opportunity.
	deliverIdx := startIdx + need - 1
	deliverTs, ok := t.oppTimeMs(deliverIdx)
	if !ok {
		return nil // schedule exhausted before packet fully served
	}

	// Backlog check: opportunities from arrIdx up to (and including) this
	// packet's delivering opportunity that are consumed by queued packets,
	// measured in bytes. If admitting this packet would push that backlog past
	// MaxBacklog, drop it (tail-drop on an over-full queue).
	if t.maxBacklog > 0 {
		queuedBytes := (deliverIdx - arrIdx + 1) * t.mtu
		if queuedBytes > t.maxBacklog {
			return nil // backlog overflow => drop
		}
	}

	deliverNs := t.baseNs + deliverTs*1_000_000
	if deliverNs < in.RecvAt {
		deliverNs = in.RecvAt // never deliver before arrival
	}

	out := in
	out.DeliverAt = deliverNs
	t.nextOpp = deliverIdx + 1
	return []engine.InFlight{out}
}

// firstOppAtOrAfter returns the index of the earliest opportunity whose
// trace-relative time is >= ms, or -1 if the (non-looping) schedule has none.
func (t *deliveryTrace) firstOppAtOrAfter(ms int64) int {
	n := len(t.sched)
	if n == 0 {
		return -1
	}
	if t.loop {
		// period is sched[n-1]; locate the cycle then binary-search within it.
		// Guard against a degenerate all-zero schedule (period 0).
		if t.periodMs <= 0 {
			// All opportunities at time 0; if ms>0 nothing is >= ms within a
			// single cycle but looping never advances time, so treat as none.
			if ms <= 0 {
				i := sort.Search(n, func(k int) bool { return t.sched[k] >= ms })
				return i
			}
			return -1
		}
		cycle := ms / t.periodMs
		rem := ms - cycle*t.periodMs
		i := sort.Search(n, func(k int) bool { return t.sched[k] >= rem })
		return int(cycle)*n + i
	}
	i := sort.Search(n, func(k int) bool { return t.sched[k] >= ms })
	if i >= n {
		return -1
	}
	return i
}

// nsToMsFloor converts a non-negative nanosecond duration to floored ms.
func nsToMsFloor(ns int64) int64 {
	if ns < 0 {
		return 0
	}
	return ns / 1_000_000
}

// ---------------------------------------------------------------------------
// Trace on-disk format: Parse + Serialize.
// ---------------------------------------------------------------------------

// Serialize writes a DeliveryTrace schedule in the documented on-disk format.
//
// Format (Mahimahi-compatible with an optional header):
//
//		# impair delivery trace v1
//		# mtu=1500 maxbacklog=65536 loop=1
//		0
//		5
//		5
//		12
//		...
//
//	  - Lines beginning with '#' are comments. The first comment may carry a
//	    key=value config line (mtu, maxbacklog, loop) which Parse reads back.
//	  - Every other non-blank line is a single integer: a delivery-opportunity
//	    timestamp in milliseconds. Repeated timestamps mean multiple
//	    opportunities (multiple MTUs) at the same instant — exactly Mahimahi's
//	    convention.
//	  - Timestamps are written in nondecreasing (sorted) order.
//
// The output round-trips through Parse.
func Serialize(cfg DeliveryTraceConfig) string {
	mtu := cfg.MTU
	if mtu <= 0 {
		mtu = defaultMTU
	}
	loop := 0
	if cfg.Loop {
		loop = 1
	}
	sched := make([]int64, len(cfg.Schedule))
	copy(sched, cfg.Schedule)
	sort.Slice(sched, func(i, j int) bool { return sched[i] < sched[j] })

	var b strings.Builder
	b.WriteString("# impair delivery trace v1\n")
	fmt.Fprintf(&b, "# mtu=%d maxbacklog=%d loop=%d\n", mtu, cfg.MaxBacklog, loop)
	for _, ts := range sched {
		fmt.Fprintf(&b, "%d\n", ts)
	}
	return b.String()
}

// Parse reads a delivery trace produced by Serialize (or any plain Mahimahi
// trace: one millisecond timestamp per line, '#' comments allowed). Config
// fields (mtu, maxbacklog, loop) are recovered from a "# key=value" comment
// line if present; otherwise they take their zero/default values.
func Parse(s string) (DeliveryTraceConfig, error) {
	var cfg DeliveryTraceConfig
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	line := 0
	var last int64
	haveLast := false
	for sc.Scan() {
		line++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" {
			continue
		}
		if strings.HasPrefix(raw, "#") {
			parseConfigComment(raw, &cfg)
			continue
		}
		ts, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return DeliveryTraceConfig{}, fmt.Errorf("droplist: trace line %d: bad timestamp %q: %w", line, raw, err)
		}
		if ts < 0 {
			return DeliveryTraceConfig{}, fmt.Errorf("droplist: trace line %d: negative timestamp %d", line, ts)
		}
		if haveLast && ts < last {
			return DeliveryTraceConfig{}, fmt.Errorf("droplist: trace line %d: timestamp %d out of order (< %d)", line, ts, last)
		}
		last = ts
		haveLast = true
		cfg.Schedule = append(cfg.Schedule, ts)
	}
	if err := sc.Err(); err != nil {
		return DeliveryTraceConfig{}, fmt.Errorf("droplist: reading trace: %w", err)
	}
	if cfg.MTU == 0 {
		cfg.MTU = defaultMTU
	}
	return cfg, nil
}

// parseConfigComment reads "key=value" tokens out of a comment line, ignoring
// anything it does not recognise (so the human-readable "v1" banner is fine).
func parseConfigComment(raw string, cfg *DeliveryTraceConfig) {
	body := strings.TrimSpace(strings.TrimPrefix(raw, "#"))
	for _, tok := range strings.Fields(body) {
		kv := strings.SplitN(tok, "=", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.ToLower(kv[0])
		val := kv[1]
		switch key {
		case "mtu":
			if n, err := strconv.Atoi(val); err == nil && n > 0 {
				cfg.MTU = n
			}
		case "maxbacklog":
			if n, err := strconv.Atoi(val); err == nil && n >= 0 {
				cfg.MaxBacklog = n
			}
		case "loop":
			cfg.Loop = val == "1" || strings.EqualFold(val, "true")
		}
	}
}

// ParseDropList parses a DropList spec of the ns-3 form "2,3,7" (whitespace and
// a leading '#' on each value tolerated, e.g. "#2, #3, #7"). It returns the
// parsed set in input order with duplicates preserved (NewDropList dedups via a
// set anyway).
func ParseDropList(s string) (DropListConfig, error) {
	var cfg DropListConfig
	for _, raw := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	}) {
		tok := strings.TrimPrefix(strings.TrimSpace(raw), "#")
		if tok == "" {
			continue
		}
		n, err := strconv.ParseUint(tok, 10, 64)
		if err != nil {
			return DropListConfig{}, fmt.Errorf("droplist: bad seq %q: %w", raw, err)
		}
		cfg.Seqs = append(cfg.Seqs, n)
	}
	return cfg, nil
}
