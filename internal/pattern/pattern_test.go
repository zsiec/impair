package pattern

import (
	"bytes"
	"strings"
	"testing"

	"github.com/zsiec/impair/engine"
)

// fixture builds a Recorder with a deterministic, representative mix of
// actions: forwards with zero and non-zero offsets, both directions, and drops.
func fixture(seed int64) *Recorder {
	r := NewRecorder(seed)
	// seq, recvAt, action
	r.Add(1000, engine.Action{Kind: engine.Forward, Dir: engine.C2S, Seq: 1, DeliverAt: 1000})
	r.Add(2000, engine.Action{Kind: engine.Forward, Dir: engine.S2C, Seq: 2, DeliverAt: 2500})
	r.Add(3000, engine.Action{Kind: engine.Drop, Dir: engine.C2S, Seq: 3, Reason: "loss"})
	r.Add(4000, engine.Action{Kind: engine.Forward, Dir: engine.C2S, Seq: 4, DeliverAt: 4000})
	r.Add(5000, engine.Action{Kind: engine.Drop, Dir: engine.S2C, Seq: 5, Reason: "burst-loss"})
	return r
}

func TestStringDeterministic(t *testing.T) {
	a := fixture(42).String()
	b := fixture(42).String()
	if a != b {
		t.Fatalf("String not deterministic:\n%q\n%q", a, b)
	}
	if a == "" {
		t.Fatal("empty output")
	}
}

func TestExactFormat(t *testing.T) {
	got := fixture(7).String()
	want := strings.Join([]string{
		"impair-pattern v1 seed=7",
		"seq=1 dir=c2s FWD off=0",
		"seq=2 dir=s2c FWD off=500",
		"seq=3 dir=c2s DROP reason=loss",
		"seq=4 dir=c2s FWD off=0",
		"seq=5 dir=s2c DROP reason=burst-loss",
		"",
	}, "\n")
	if got != want {
		t.Fatalf("format mismatch:\n got=%q\nwant=%q", got, want)
	}
}

func TestWriteToMatchesString(t *testing.T) {
	r := fixture(99)
	var buf bytes.Buffer
	n, err := r.WriteTo(&buf)
	if err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	if int(n) != buf.Len() {
		t.Fatalf("WriteTo returned n=%d but wrote %d bytes", n, buf.Len())
	}
	if buf.String() != r.String() {
		t.Fatalf("WriteTo != String:\n%q\n%q", buf.String(), r.String())
	}
}

func TestNegativeOffsetClamped(t *testing.T) {
	r := NewRecorder(1)
	// DeliverAt < recvAt should never happen per engine contract, but the
	// recorder must not emit a negative offset if it does.
	r.Add(5000, engine.Action{Kind: engine.Forward, Dir: engine.C2S, Seq: 1, DeliverAt: 4000})
	if !strings.Contains(r.String(), "off=0") {
		t.Fatalf("expected clamped off=0, got %q", r.String())
	}
}

func TestRoundTrip(t *testing.T) {
	orig := fixture(123).String()
	p, err := Parse(strings.NewReader(orig))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.Version != Version {
		t.Fatalf("version=%d want %d", p.Version, Version)
	}
	if p.Seed != 123 {
		t.Fatalf("seed=%d want 123", p.Seed)
	}
	if len(p.Records) != 5 {
		t.Fatalf("records=%d want 5", len(p.Records))
	}

	// Re-serialize via a fresh Recorder constructed from the parsed Pattern.
	r2 := NewRecorder(p.Seed)
	for _, rec := range p.Records {
		switch rec.Disp {
		case Forward:
			r2.Add(0, engine.Action{Kind: engine.Forward, Dir: rec.Dir, Seq: rec.Seq, DeliverAt: rec.Offset})
		case Drop:
			r2.Add(0, engine.Action{Kind: engine.Drop, Dir: rec.Dir, Seq: rec.Seq, Reason: rec.Reason})
		}
	}
	if got := r2.String(); got != orig {
		t.Fatalf("round-trip not stable:\norig=%q\n got=%q", orig, got)
	}
}

func TestParseRecordsContent(t *testing.T) {
	p, err := Parse(strings.NewReader(fixture(5).String()))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []Record{
		{Seq: 1, Dir: engine.C2S, Disp: Forward, Offset: 0},
		{Seq: 2, Dir: engine.S2C, Disp: Forward, Offset: 500},
		{Seq: 3, Dir: engine.C2S, Disp: Drop, Reason: "loss"},
		{Seq: 4, Dir: engine.C2S, Disp: Forward, Offset: 0},
		{Seq: 5, Dir: engine.S2C, Disp: Drop, Reason: "burst-loss"},
	}
	if len(p.Records) != len(want) {
		t.Fatalf("got %d records, want %d", len(p.Records), len(want))
	}
	for i := range want {
		if p.Records[i] != want[i] {
			t.Fatalf("record %d = %+v, want %+v", i, p.Records[i], want[i])
		}
	}
}

func TestParseToleratesTrailingBlank(t *testing.T) {
	in := fixture(1).String() + "\n\n"
	p, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(p.Records) != 5 {
		t.Fatalf("records=%d want 5", len(p.Records))
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"bad header tag", "wrong v1 seed=1\n"},
		{"bad version", "impair-pattern vX seed=1\n"},
		{"bad seed", "impair-pattern v1 seed=abc\n"},
		{"missing seed", "impair-pattern v1\n"},
		{"bad seq", "impair-pattern v1 seed=1\nseq=x dir=c2s FWD off=0\n"},
		{"bad dir", "impair-pattern v1 seed=1\nseq=1 dir=xxx FWD off=0\n"},
		{"bad disposition", "impair-pattern v1 seed=1\nseq=1 dir=c2s ZAP off=0\n"},
		{"bad offset", "impair-pattern v1 seed=1\nseq=1 dir=c2s FWD off=nope\n"},
		{"negative offset", "impair-pattern v1 seed=1\nseq=1 dir=c2s FWD off=-5\n"},
		{"missing off prefix", "impair-pattern v1 seed=1\nseq=1 dir=c2s FWD 0\n"},
		{"missing reason prefix", "impair-pattern v1 seed=1\nseq=1 dir=c2s DROP loss\n"},
		{"wrong field count", "impair-pattern v1 seed=1\nseq=1 dir=c2s FWD\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Parse(strings.NewReader(c.in)); err == nil {
				t.Fatalf("expected error for %q", c.in)
			}
		})
	}
}

func TestCompareIdentical(t *testing.T) {
	a := fixture(1).String()
	b := fixture(1).String()
	if d, diff := Compare(a, b); diff {
		t.Fatalf("identical artifacts reported diff at line %d: %+v", d.Line, d)
	}
}

func TestCompareSingleLineDiff(t *testing.T) {
	base := fixture(1)
	a := base.String()

	// Mutate exactly one record (seq 4's offset) -> differs on its line only.
	mod := NewRecorder(1)
	mod.Add(1000, engine.Action{Kind: engine.Forward, Dir: engine.C2S, Seq: 1, DeliverAt: 1000})
	mod.Add(2000, engine.Action{Kind: engine.Forward, Dir: engine.S2C, Seq: 2, DeliverAt: 2500})
	mod.Add(3000, engine.Action{Kind: engine.Drop, Dir: engine.C2S, Seq: 3, Reason: "loss"})
	mod.Add(4000, engine.Action{Kind: engine.Forward, Dir: engine.C2S, Seq: 4, DeliverAt: 4123})
	mod.Add(5000, engine.Action{Kind: engine.Drop, Dir: engine.S2C, Seq: 5, Reason: "burst-loss"})
	b := mod.String()

	d, diff := Compare(a, b)
	if !diff {
		t.Fatal("expected a diff")
	}
	// Header(1) + 3 unchanged records => first diff at line 5 (seq=4 record).
	if d.Line != 5 {
		t.Fatalf("diff line=%d want 5 (%+v)", d.Line, d)
	}
	if !strings.Contains(d.A, "off=0") || !strings.Contains(d.B, "off=123") {
		t.Fatalf("diff content wrong: A=%q B=%q", d.A, d.B)
	}
}

func TestCompareHeaderDiff(t *testing.T) {
	a := fixture(1).String()
	b := fixture(2).String()
	d, diff := Compare(a, b)
	if !diff {
		t.Fatal("expected diff on differing seed")
	}
	if d.Line != 1 {
		t.Fatalf("header diff line=%d want 1", d.Line)
	}
}

func TestCompareLengthDiff(t *testing.T) {
	full := fixture(1).String()

	short := NewRecorder(1)
	short.Add(1000, engine.Action{Kind: engine.Forward, Dir: engine.C2S, Seq: 1, DeliverAt: 1000})
	shortStr := short.String()

	d, diff := Compare(full, shortStr)
	if !diff {
		t.Fatal("expected diff for differing lengths")
	}
	// First two lines (header + seq=1 FWD) match; line 3 exists only in full.
	if d.Line != 3 {
		t.Fatalf("length diff line=%d want 3 (%+v)", d.Line, d)
	}
	if d.B != "" {
		t.Fatalf("missing side B should be empty, got %q", d.B)
	}
}

func TestCompareTrailingNewlineInsensitive(t *testing.T) {
	a := fixture(1).String()
	b := strings.TrimSuffix(a, "\n")
	if d, diff := Compare(a, b); diff {
		t.Fatalf("trailing newline should not matter, diff at %d", d.Line)
	}
}

func TestLen(t *testing.T) {
	r := NewRecorder(0)
	if r.Len() != 0 {
		t.Fatalf("Len=%d want 0", r.Len())
	}
	r.Add(0, engine.Action{Kind: engine.Forward, Dir: engine.C2S, Seq: 1, DeliverAt: 0})
	if r.Len() != 1 {
		t.Fatalf("Len=%d want 1", r.Len())
	}
}

// TestEngineIntegration drives real engine.Actions through the recorder to
// confirm the artifact captures a live pipeline deterministically.
func TestEngineIntegration(t *testing.T) {
	build := func() string {
		eng := engine.New(
			[]engine.Cell{dropEven{}},
			[]engine.Cell{nop{}},
		)
		rec := NewRecorder(2024)
		for i := 0; i < 20; i++ {
			dir := engine.C2S
			if i%3 == 0 {
				dir = engine.S2C
			}
			at := int64(i) * 1000
			data := []byte{byte(i)}
			for _, a := range eng.Handle(engine.Packet{Data: data, Dir: dir}, at) {
				rec.Add(at, a)
			}
		}
		return rec.String()
	}
	a := build()
	b := build()
	if a != b {
		t.Fatal("engine-driven artifact not deterministic")
	}
	if _, err := Parse(strings.NewReader(a)); err != nil {
		t.Fatalf("Parse of engine artifact: %v", err)
	}
}

type nop struct{}

func (nop) Name() string                                 { return "nop" }
func (nop) Process(in engine.InFlight) []engine.InFlight { return []engine.InFlight{in} }

type dropEven struct{}

func (dropEven) Name() string { return "drop-even" }
func (dropEven) Process(in engine.InFlight) []engine.InFlight {
	if in.Seq%2 == 0 {
		return nil
	}
	return []engine.InFlight{in}
}
