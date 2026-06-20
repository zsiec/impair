package report

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/zsiec/impair/result"
)

var dataIsland = regexp.MustCompile(`(?s)<script type="application/json" id="tw-data">(.*?)</script>`)

func TestWriteDashboard(t *testing.T) {
	m := result.NewMatrix("m", "2026-06-20T00:00:00Z", []result.Result{
		{Lib: "a", Scenario: "clean", Checks: []result.Check{{Name: "c1", Verdict: result.Pass, Detail: "ok"}}, Metrics: map[string]float64{"x": 1.5}},
		{Lib: "b", Scenario: "clean", Checks: []result.Check{{Name: "c1", Verdict: result.Fail, Detail: "bad"}}},
	})
	var buf bytes.Buffer
	if err := WriteDashboard(&buf, "Title", "gen", []DashboardSection{
		{Title: "Cap", Tier: "Tier-1 bit-deterministic", Blurb: "b", Matrix: m},
	}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	// The embedded JSON island must round-trip back to the data.
	mm := dataIsland.FindStringSubmatch(out)
	if mm == nil {
		t.Fatal("no embedded data island")
	}
	var got dashboardData
	if err := json.Unmarshal([]byte(mm[1]), &got); err != nil {
		t.Fatalf("embedded JSON does not parse: %v", err)
	}
	if got.Title != "Title" || len(got.Sections) != 1 || len(got.Sections[0].Matrix.Results) != 2 {
		t.Fatalf("round-trip lost data: %+v", got)
	}

	// Self-contained: no external asset references.
	if regexp.MustCompile(`(?:src|href)="https?://`).MatchString(out) {
		t.Fatal("dashboard references an external asset")
	}
}

// A detail (or any string) containing "</script>" must not break out of the JSON
// island — encoding/json escapes "<" so the literal sequence never appears raw.
func TestWriteDashboardScriptEscape(t *testing.T) {
	m := result.NewMatrix("m", "g", []result.Result{
		{Lib: "x", Scenario: "s", Checks: []result.Check{{Name: "n", Verdict: result.Warn, Detail: "evil </script><img src=x>"}}},
	})
	var buf bytes.Buffer
	if err := WriteDashboard(&buf, "t", "g", []DashboardSection{{Title: "c", Tier: "Tier-2", Blurb: "", Matrix: m}}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	// Exactly one real closing tag for the data island; the payload's "</script>"
	// is escaped to </script> and cannot terminate the script element.
	if strings.Contains(out, "evil </script>") {
		t.Fatal("payload </script> was not escaped — script-injection possible")
	}
}
