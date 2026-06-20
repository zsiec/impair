// Package report renders a result.Matrix into shareable artifacts: an indented
// JSON dump and a self-contained, static HTML conformance grid (quic-interop /
// wpt.fyi style) with no external assets. It depends only on internal/result
// and the standard library.
package report

import (
	"encoding/json"
	"html/template"
	"io"
	"sort"

	"github.com/zsiec/impair/result"
)

// WriteJSON writes m as indented JSON.
func WriteJSON(w io.Writer, m result.Matrix) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(m)
}

// WriteHTML writes a self-contained static HTML page rendering m as a colored
// conformance grid: rows are scenarios, columns are libs. Each cell shows the
// run's rollup verdict, colored by severity, with the per-check breakdown and
// metrics available on hover and in an expandable detail panel.
func WriteHTML(w io.Writer, m result.Matrix) error {
	return htmlTemplate.Execute(w, buildView(m))
}

// --- view model -------------------------------------------------------------

type cellView struct {
	Present  bool
	Lib      string
	Scenario string
	Verdict  string // PASS/WARN/FAIL/UNSUPPORTED/ERROR/—
	Class    string // CSS class for coloring
	Title    string // hover summary (title attribute)
	Checks   []checkView
	Metrics  []metricView
	Err      string
}

type checkView struct {
	Name    string
	Verdict string
	Class   string
	Detail  string
}

type metricView struct {
	Key   string
	Value float64
}

type rowView struct {
	Scenario string
	Cells    []cellView
}

type legendItem struct {
	Label string
	Class string
}

type matrixView struct {
	Title     string
	Generated string
	Libs      []string
	Rows      []rowView
	Legend    []legendItem
}

func verdictClass(v result.Verdict) string {
	switch v {
	case result.Pass:
		return "v-pass"
	case result.Warn:
		return "v-warn"
	case result.Fail:
		return "v-fail"
	case result.Unsupported:
		return "v-unsupported"
	case result.Error:
		return "v-error"
	default:
		return "v-none"
	}
}

func buildView(m result.Matrix) matrixView {
	mv := matrixView{
		Title:     m.Title,
		Generated: m.Generated,
		Libs:      m.Libs,
		Legend: []legendItem{
			{Label: result.Pass.String(), Class: verdictClass(result.Pass)},
			{Label: result.Warn.String(), Class: verdictClass(result.Warn)},
			{Label: result.Fail.String(), Class: verdictClass(result.Fail)},
			{Label: result.Unsupported.String(), Class: verdictClass(result.Unsupported)},
			{Label: result.Error.String(), Class: verdictClass(result.Error)},
		},
	}

	for _, scn := range m.Scenarios {
		row := rowView{Scenario: scn}
		for _, lib := range m.Libs {
			row.Cells = append(row.Cells, buildCell(m, lib, scn))
		}
		mv.Rows = append(mv.Rows, row)
	}
	return mv
}

func buildCell(m result.Matrix, lib, scn string) cellView {
	res, ok := m.Get(lib, scn)
	if !ok {
		return cellView{
			Present:  false,
			Lib:      lib,
			Scenario: scn,
			Verdict:  "—",
			Class:    "v-none",
			Title:    lib + " / " + scn + ": no run",
		}
	}

	rollup := res.Verdict()
	cell := cellView{
		Present:  true,
		Lib:      lib,
		Scenario: scn,
		Verdict:  rollup.String(),
		Class:    verdictClass(rollup),
		Err:      res.Err,
	}

	for _, c := range res.Checks {
		cell.Checks = append(cell.Checks, checkView{
			Name:    c.Name,
			Verdict: c.Verdict.String(),
			Class:   verdictClass(c.Verdict),
			Detail:  c.Detail,
		})
	}

	// Stable metric ordering for deterministic output.
	keys := make([]string, 0, len(res.Metrics))
	for k := range res.Metrics {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		cell.Metrics = append(cell.Metrics, metricView{Key: k, Value: res.Metrics[k]})
	}

	// Build the hover title: rollup + each check.
	title := lib + " / " + scn + ": " + rollup.String()
	for _, c := range cell.Checks {
		title += "\n  " + c.Verdict + " " + c.Name
		if c.Detail != "" {
			title += " — " + c.Detail
		}
	}
	if res.Err != "" {
		title += "\n  error: " + res.Err
	}
	cell.Title = title

	return cell
}

var htmlTemplate = template.Must(template.New("matrix").Parse(htmlSource))

const htmlSource = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
  :root {
    --pass: #1b873f; --warn: #b8860b; --fail: #c1121f;
    --unsupported: #6b7280; --error: #7a0d12; --none: #d1d5db;
    --bg: #0f1115; --panel: #181b22; --ink: #e7e9ee; --muted: #9aa3b2;
    --grid: #2a2f3a;
  }
  * { box-sizing: border-box; }
  body {
    margin: 0; padding: 2rem;
    background: var(--bg); color: var(--ink);
    font: 14px/1.45 -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
  }
  h1 { margin: 0 0 .25rem; font-size: 1.5rem; }
  .generated { color: var(--muted); margin: 0 0 1.5rem; font-size: .85rem; }
  .legend { display: flex; gap: .75rem; flex-wrap: wrap; margin: 0 0 1.5rem; }
  .legend .chip { display: inline-flex; align-items: center; gap: .4rem; font-size: .8rem; color: var(--muted); }
  .legend .swatch { width: 14px; height: 14px; border-radius: 3px; display: inline-block; }
  table { border-collapse: separate; border-spacing: 0; width: 100%; max-width: 100%; }
  th, td { border: 1px solid var(--grid); padding: 0; text-align: center; }
  thead th {
    background: var(--panel); color: var(--ink); padding: .6rem .8rem;
    font-weight: 600; position: sticky; top: 0; z-index: 2;
  }
  thead th.corner { text-align: left; }
  tbody th.scenario {
    background: var(--panel); text-align: left; padding: .6rem .8rem;
    font-weight: 600; white-space: nowrap; position: sticky; left: 0; z-index: 1;
  }
  td.cell { min-width: 120px; }
  details { margin: 0; }
  summary {
    list-style: none; cursor: pointer; padding: .7rem .8rem;
    font-weight: 700; letter-spacing: .02em; color: #fff;
    display: block;
  }
  summary::-webkit-details-marker { display: none; }
  details[open] summary { box-shadow: inset 0 -2px 0 rgba(0,0,0,.25); }
  .v-pass        > summary, .swatch.v-pass        { background: var(--pass); }
  .v-warn        > summary, .swatch.v-warn        { background: var(--warn); }
  .v-fail        > summary, .swatch.v-fail        { background: var(--fail); }
  .v-unsupported > summary, .swatch.v-unsupported { background: var(--unsupported); }
  .v-error       > summary, .swatch.v-error       { background: var(--error); }
  td.v-none { background: repeating-linear-gradient(45deg, transparent, transparent 6px, rgba(255,255,255,.04) 6px, rgba(255,255,255,.04) 12px); color: var(--muted); padding: .7rem .8rem; }
  .detail { padding: .6rem .8rem; background: var(--panel); text-align: left; color: var(--ink); }
  .detail ul { margin: 0; padding: 0; list-style: none; }
  .detail li { padding: .15rem 0; display: flex; gap: .5rem; align-items: baseline; }
  .badge { font-size: .65rem; font-weight: 700; padding: .1rem .35rem; border-radius: 3px; color: #fff; white-space: nowrap; }
  .badge.v-pass { background: var(--pass); }
  .badge.v-warn { background: var(--warn); }
  .badge.v-fail { background: var(--fail); }
  .badge.v-unsupported { background: var(--unsupported); }
  .badge.v-error { background: var(--error); }
  .cname { font-weight: 600; }
  .cdetail { color: var(--muted); }
  .metrics { margin-top: .5rem; border-top: 1px solid var(--grid); padding-top: .4rem; }
  .metrics .m { display: flex; justify-content: space-between; gap: 1rem; color: var(--muted); font-variant-numeric: tabular-nums; }
  .metrics .m .mk { color: var(--ink); }
  .err { margin-top: .5rem; color: #ff9aa0; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: .8rem; }
</style>
</head>
<body>
  <h1>{{.Title}}</h1>
  <p class="generated">Generated: {{.Generated}}</p>

  <div class="legend">
    {{range .Legend}}<span class="chip"><span class="swatch {{.Class}}"></span>{{.Label}}</span>{{end}}
  </div>

  <table>
    <thead>
      <tr>
        <th class="corner">Scenario \ Library</th>
        {{range .Libs}}<th>{{.}}</th>{{end}}
      </tr>
    </thead>
    <tbody>
      {{range .Rows}}
      <tr>
        <th class="scenario">{{.Scenario}}</th>
        {{range .Cells}}
        {{if .Present}}
        <td class="cell">
          <details title="{{.Title}}">
            <summary class="{{.Class}}">{{.Verdict}}</summary>
            <div class="detail">
              <ul>
                {{range .Checks}}
                <li>
                  <span class="badge {{.Class}}">{{.Verdict}}</span>
                  <span class="cname">{{.Name}}</span>
                  {{if .Detail}}<span class="cdetail">— {{.Detail}}</span>{{end}}
                </li>
                {{else}}
                <li class="cdetail">no checks recorded</li>
                {{end}}
              </ul>
              {{if .Metrics}}
              <div class="metrics">
                {{range .Metrics}}<div class="m"><span class="mk">{{.Key}}</span><span>{{printf "%g" .Value}}</span></div>{{end}}
              </div>
              {{end}}
              {{if .Err}}<div class="err">error: {{.Err}}</div>{{end}}
            </div>
          </details>
        </td>
        {{else}}
        <td class="cell v-none" title="{{.Title}}">{{.Verdict}}</td>
        {{end}}
        {{end}}
      </tr>
      {{end}}
    </tbody>
  </table>
</body>
</html>
`
