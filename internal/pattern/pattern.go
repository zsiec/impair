// Package pattern is the versioned record/replay artifact for Impair — the
// golden-diff substrate. A Recorder consumes the engine.Action values produced
// by an Engine and serializes the realized schedule to a STABLE, versioned,
// human-diffable text format: a header line carrying a format-version constant
// and the master seed, then one deterministic line per action.
//
// The format is intentionally line-oriented and field-tagged so that a CI
// golden-diff (see Compare) points at the exact first line that changed when a
// cell's behaviour drifts. Parse reverses the serialization into a structured
// Pattern, and serialize->parse->serialize is a stable round-trip.
//
// This package reads no clock and holds no randomness: it is a pure function of
// the (seed, action sequence) it is fed, so the artifact is byte-identical
// across runs and machines — the P0 determinism property, made auditable.
package pattern

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/zsiec/impair/internal/engine"
)

// Version is the format-version constant emitted in the header. Bump this only
// when the on-disk line grammar changes incompatibly; golden files pin it.
const Version = 1

// header is the literal token that opens every artifact.
const headerTag = "impair-pattern"

// Disposition is the realized fate of an ingress packet in a Record.
type Disposition uint8

const (
	// Forward = the packet was forwarded; Offset carries deliver-minus-recv ns.
	Forward Disposition = iota
	// Drop = the packet was dropped; Reason carries the dropping cell's name.
	Drop
)

func (d Disposition) String() string {
	switch d {
	case Forward:
		return "FWD"
	case Drop:
		return "DROP"
	default:
		return "?"
	}
}

// Record is one serialized action line in structured form.
type Record struct {
	Seq    uint64           // ingress id (engine-assigned; duplicates share it)
	Dir    engine.Direction // travel direction
	Disp   Disposition      // FWD or DROP
	Offset int64            // FWD: DeliverAt-RecvAt (ns, >=0); DROP: 0
	Reason string           // DROP: dropping cell name; FWD: ""
}

// Pattern is a parsed artifact: its header fields plus the ordered records.
type Pattern struct {
	Version int
	Seed    int64
	Records []Record
}

// Recorder accumulates Records and renders them to the stable text format.
// The zero value is not usable; construct one with NewRecorder so the seed is
// captured in the header.
type Recorder struct {
	seed    int64
	records []Record
}

// NewRecorder returns a Recorder that will stamp the given master seed into the
// artifact header. Pass the seed from rng.Root.Seed() so the artifact is
// self-describing and a reader can reproduce the run.
func NewRecorder(seed int64) *Recorder {
	return &Recorder{seed: seed}
}

// Add records one engine.Action realized at recvAt (the packet's virtual
// ingress time, needed to derive the Forward deliver-offset; engine.Action does
// not itself carry RecvAt). For a Drop the recvAt is ignored. A Forward whose
// DeliverAt precedes recvAt is clamped to a zero offset rather than emitting a
// negative number, since the engine contract forbids DeliverAt < RecvAt and a
// negative offset would only be noise in a golden diff.
func (r *Recorder) Add(recvAt int64, a engine.Action) {
	rec := Record{Seq: a.Seq, Dir: a.Dir}
	switch a.Kind {
	case engine.Drop:
		rec.Disp = Drop
		rec.Reason = a.Reason
	default: // engine.Forward
		rec.Disp = Forward
		off := a.DeliverAt - recvAt
		if off < 0 {
			off = 0
		}
		rec.Offset = off
	}
	r.records = append(r.records, rec)
}

// Len reports how many records have been added.
func (r *Recorder) Len() int { return len(r.records) }

// WriteTo serializes the artifact to w. It implements io.WriterTo. The output
// is deterministic for a fixed (seed, record) sequence: a header line followed
// by one line per record, each terminated by '\n'.
func (r *Recorder) WriteTo(w io.Writer) (int64, error) {
	cw := &countWriter{w: w}
	bw := bufio.NewWriter(cw)
	writeHeader(bw, r.seed)
	for i := range r.records {
		writeRecord(bw, &r.records[i])
	}
	if err := bw.Flush(); err != nil {
		return cw.n, err
	}
	return cw.n, nil
}

// String renders the artifact as a string (the golden-diff value).
func (r *Recorder) String() string {
	var b strings.Builder
	writeHeader(&b, r.seed)
	for i := range r.records {
		writeRecord(&b, &r.records[i])
	}
	return b.String()
}

// writeHeader emits the version+seed header line.
func writeHeader(w io.StringWriter, seed int64) {
	w.WriteString(headerTag)
	w.WriteString(" v")
	w.WriteString(strconv.Itoa(Version))
	w.WriteString(" seed=")
	w.WriteString(strconv.FormatInt(seed, 10))
	w.WriteString("\n")
}

// writeRecord emits one record line in the stable, field-tagged grammar.
//
//	FWD:  seq=<n> dir=<c2s|s2c> FWD off=<ns>
//	DROP: seq=<n> dir=<c2s|s2c> DROP reason=<name>
func writeRecord(w io.StringWriter, rec *Record) {
	w.WriteString("seq=")
	w.WriteString(strconv.FormatUint(rec.Seq, 10))
	w.WriteString(" dir=")
	w.WriteString(rec.Dir.String())
	w.WriteString(" ")
	w.WriteString(rec.Disp.String())
	switch rec.Disp {
	case Drop:
		w.WriteString(" reason=")
		w.WriteString(rec.Reason)
	default:
		w.WriteString(" off=")
		w.WriteString(strconv.FormatInt(rec.Offset, 10))
	}
	w.WriteString("\n")
}

// countWriter counts bytes written through it for WriteTo's int64 return.
type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// Parse reads a serialized artifact back into a Pattern. It is the inverse of
// Recorder serialization: Parse(serialize(p)) reproduces p, and re-serializing
// the result is byte-identical (round-trip stable). It is strict — any line it
// cannot interpret yields an error naming the 1-based line number — so a
// corrupt or hand-edited golden file fails loudly rather than silently.
func Parse(rd io.Reader) (*Pattern, error) {
	sc := bufio.NewScanner(rd)
	// Pattern lines are short, but allow generous room for long cell names.
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	line := 0
	var p Pattern
	haveHeader := false
	for sc.Scan() {
		line++
		text := sc.Text()
		if !haveHeader {
			v, seed, err := parseHeader(text)
			if err != nil {
				return nil, fmt.Errorf("pattern: line %d: %w", line, err)
			}
			p.Version = v
			p.Seed = seed
			haveHeader = true
			continue
		}
		if text == "" {
			continue // tolerate a trailing blank line
		}
		rec, err := parseRecord(text)
		if err != nil {
			return nil, fmt.Errorf("pattern: line %d: %w", line, err)
		}
		p.Records = append(p.Records, rec)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("pattern: read: %w", err)
	}
	if !haveHeader {
		return nil, fmt.Errorf("pattern: missing header")
	}
	return &p, nil
}

// parseHeader parses "impair-pattern v<N> seed=<n>".
func parseHeader(text string) (version int, seed int64, err error) {
	fields := strings.Fields(text)
	if len(fields) != 3 || fields[0] != headerTag {
		return 0, 0, fmt.Errorf("bad header %q", text)
	}
	if !strings.HasPrefix(fields[1], "v") {
		return 0, 0, fmt.Errorf("bad version field %q", fields[1])
	}
	version, err = strconv.Atoi(fields[1][1:])
	if err != nil {
		return 0, 0, fmt.Errorf("bad version %q: %w", fields[1], err)
	}
	sv, ok := strings.CutPrefix(fields[2], "seed=")
	if !ok {
		return 0, 0, fmt.Errorf("missing seed= in %q", fields[2])
	}
	seed, err = strconv.ParseInt(sv, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("bad seed %q: %w", sv, err)
	}
	return version, seed, nil
}

// parseRecord parses one record line into a Record.
func parseRecord(text string) (Record, error) {
	fields := strings.Fields(text)
	if len(fields) != 4 {
		return Record{}, fmt.Errorf("expected 4 fields, got %d in %q", len(fields), text)
	}
	var rec Record

	sv, ok := strings.CutPrefix(fields[0], "seq=")
	if !ok {
		return Record{}, fmt.Errorf("missing seq= in %q", fields[0])
	}
	seq, err := strconv.ParseUint(sv, 10, 64)
	if err != nil {
		return Record{}, fmt.Errorf("bad seq %q: %w", sv, err)
	}
	rec.Seq = seq

	dv, ok := strings.CutPrefix(fields[1], "dir=")
	if !ok {
		return Record{}, fmt.Errorf("missing dir= in %q", fields[1])
	}
	dir, err := parseDir(dv)
	if err != nil {
		return Record{}, err
	}
	rec.Dir = dir

	switch fields[2] {
	case "FWD":
		rec.Disp = Forward
		ov, ok := strings.CutPrefix(fields[3], "off=")
		if !ok {
			return Record{}, fmt.Errorf("missing off= in %q", fields[3])
		}
		off, err := strconv.ParseInt(ov, 10, 64)
		if err != nil {
			return Record{}, fmt.Errorf("bad off %q: %w", ov, err)
		}
		if off < 0 {
			return Record{}, fmt.Errorf("negative off %d", off)
		}
		rec.Offset = off
	case "DROP":
		rec.Disp = Drop
		rv, ok := strings.CutPrefix(fields[3], "reason=")
		if !ok {
			return Record{}, fmt.Errorf("missing reason= in %q", fields[3])
		}
		rec.Reason = rv
	default:
		return Record{}, fmt.Errorf("bad disposition %q", fields[2])
	}
	return rec, nil
}

// parseDir maps the textual direction back to engine.Direction.
func parseDir(s string) (engine.Direction, error) {
	switch s {
	case "c2s":
		return engine.C2S, nil
	case "s2c":
		return engine.S2C, nil
	default:
		return 0, fmt.Errorf("bad dir %q", s)
	}
}

// Diff is the result of a Compare: where two artifacts first differ.
type Diff struct {
	// Line is the 1-based line number of the first difference (1 == header).
	Line int
	// A and B are the differing lines from each input. When one input is
	// shorter, the missing side is reported as the empty string.
	A, B string
}

// Compare reports the first line at which serialized artifacts a and b differ.
// It returns (nil, false) when they are byte-identical, or (*Diff, true) naming
// the first divergent line — the primitive CI uses to golden-diff a recorded
// pattern against its checked-in baseline and point at the exact regression.
//
// Comparison is line-based on the rendered text so the header is line 1 and a
// changed seed or version surfaces there; record differences surface at their
// own line. Both inputs are compared verbatim, including the header.
func Compare(a, b string) (*Diff, bool) {
	la := splitLines(a)
	lb := splitLines(b)
	n := len(la)
	if len(lb) > n {
		n = len(lb)
	}
	for i := 0; i < n; i++ {
		var sa, sb string
		if i < len(la) {
			sa = la[i]
		}
		if i < len(lb) {
			sb = lb[i]
		}
		if sa != sb {
			return &Diff{Line: i + 1, A: sa, B: sb}, true
		}
	}
	return nil, false
}

// splitLines splits s into lines, dropping a single trailing newline so that
// "x\n" and "x" compare equal and a final empty element is not produced.
func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
