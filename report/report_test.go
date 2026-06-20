package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zsiec/impair/result"
)

func sampleMatrix() result.Matrix {
	results := []result.Result{
		{
			Lib:      "libsrt",
			Scenario: "loss-5pct",
			Checks: []result.Check{
				{Name: "delivery", Verdict: result.Pass, Detail: "100% delivered"},
				{Name: "ordering", Verdict: result.Pass},
			},
			Metrics: map[string]float64{"goodput": 4.2, "retransmits": 13},
		},
		{
			Lib:      "libsrt",
			Scenario: "reorder",
			Checks: []result.Check{
				{Name: "ordering", Verdict: result.Warn, Detail: "1 late packet"},
			},
		},
		{
			Lib:      "srtgo",
			Scenario: "loss-5pct",
			Checks: []result.Check{
				{Name: "delivery", Verdict: result.Fail, Detail: "dropped seq 42"},
			},
		},
		{
			Lib:      "srtgo",
			Scenario: "reorder",
			Err:      "handshake timeout",
		},
	}
	return result.NewMatrix("SRT Conformance Matrix", "2026-06-19T12:00:00Z", results)
}

func TestWriteHTML(t *testing.T) {
	m := sampleMatrix()
	var buf bytes.Buffer
	if err := WriteHTML(&buf, m); err != nil {
		t.Fatalf("WriteHTML: %v", err)
	}
	out := buf.String()

	wants := []string{
		"SRT Conformance Matrix", // title
		"2026-06-19T12:00:00Z",   // generated
		"libsrt", "srtgo",        // libs
		"loss-5pct", "reorder", // scenarios
		"PASS", "WARN", "FAIL", "ERROR", "UNSUPPORTED", // verdict labels (legend + cells)
		"handshake timeout", // error surfaced
		"<!DOCTYPE html>",   // genuine page
		"<style>",           // self-contained styles
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("HTML missing %q", w)
		}
	}

	// No external assets.
	for _, bad := range []string{"http://", "https://", "src=", "<link"} {
		if strings.Contains(out, bad) {
			t.Errorf("HTML should be self-contained, found %q", bad)
		}
	}
}

func TestWriteHTMLEscapesUserStrings(t *testing.T) {
	results := []result.Result{
		{
			Lib:      "<lib>",
			Scenario: "scn&1",
			Checks:   []result.Check{{Name: "c", Verdict: result.Fail, Detail: "<script>x</script>"}},
		},
	}
	m := result.NewMatrix("T", "now", results)
	var buf bytes.Buffer
	if err := WriteHTML(&buf, m); err != nil {
		t.Fatalf("WriteHTML: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "<script>x</script>") {
		t.Errorf("user detail was not escaped")
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Errorf("expected escaped detail, got:\n%s", out)
	}
}

func TestWriteJSONRoundTrip(t *testing.T) {
	m := sampleMatrix()
	var buf bytes.Buffer
	if err := WriteJSON(&buf, m); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}

	if !bytes.Contains(buf.Bytes(), []byte("  ")) {
		t.Errorf("expected indented JSON")
	}

	var got result.Matrix
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.Title != m.Title || got.Generated != m.Generated {
		t.Errorf("title/generated mismatch: %+v", got)
	}
	if len(got.Libs) != len(m.Libs) || len(got.Scenarios) != len(m.Scenarios) {
		t.Fatalf("axes mismatch: libs=%v scns=%v", got.Libs, got.Scenarios)
	}

	// Verdicts must round-trip identically for every cell.
	for _, lib := range m.Libs {
		for _, scn := range m.Scenarios {
			want, wok := m.Get(lib, scn)
			have, hok := got.Get(lib, scn)
			if wok != hok {
				t.Errorf("presence mismatch at %s/%s", lib, scn)
				continue
			}
			if want.Verdict() != have.Verdict() {
				t.Errorf("verdict mismatch at %s/%s: want %v have %v",
					lib, scn, want.Verdict(), have.Verdict())
			}
		}
	}
}
