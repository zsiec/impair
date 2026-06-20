package report

import (
	"encoding/json"
	"io"
	"text/template"

	"github.com/zsiec/impair/result"
)

// DashboardSection is one capability matrix on the unified dashboard: a graded
// result.Matrix plus its tier and a one-line description.
type DashboardSection struct {
	Title  string        `json:"title"`
	Tier   string        `json:"tier"`  // e.g. "Tier-1 bit-deterministic" / "Tier-2 distribution-reproducible"
	Blurb  string        `json:"blurb"` // one-line description of what the section certifies
	Matrix result.Matrix `json:"matrix"`
	// Frames is an optional per-cell "what survived" filmstrip sidecar for
	// glass-to-glass sections (lib -> scenario -> {thumbs, deliveredPct, audioPct,
	// keyframes, ...}). Passed through verbatim into the data island and rendered
	// client-side as a scrubbable tape; nil for sections without baked frames.
	Frames json.RawMessage `json:"frames,omitempty"`
}

type dashboardData struct {
	Title     string             `json:"title"`
	Generated string             `json:"generated"`
	Sections  []DashboardSection `json:"sections"`
}

// WriteDashboard renders a single self-contained static HTML dashboard that
// aggregates several capability matrices into one cross-protocol conformance
// view — a "Signal Bench" instrument readout. The data is embedded as JSON and
// rendered client-side by vanilla JS; there are no external assets.
func WriteDashboard(w io.Writer, title, generated string, sections []DashboardSection) error {
	js, err := json.Marshal(dashboardData{Title: title, Generated: generated, Sections: sections})
	if err != nil {
		return err
	}
	// text/template: the JSON (with <,>,& escaped by encoding/json) is injected
	// verbatim into a <script type="application/json"> island, so a "</script>"
	// in any detail string is already <-escaped and cannot break out.
	return dashboardTemplate.Execute(w, string(js))
}

var dashboardTemplate = template.Must(template.New("dashboard").Parse(dashboardSource))

const dashboardSource = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Transit-WPT · Signal Bench</title>
<script type="application/json" id="tw-data">{{.}}</script>
<style>
  /* International-Typographic light theme: paper ground, hairline rules, ink +
     a lone red accent. Colour appears ONLY as signal (verdicts, curves). */
  :root {
    --bg:#fcfcfa; --bg2:#f4f4f0; --panel:#ffffff; --panel2:#f7f7f3; --line:#e6e6e0; --line2:#cdccc4;
    --ink:#17191e; --ink2:#000000; --muted:#646a72; --faint:#9aa0a6;
    --accent:#e5341e; /* Swiss red — the lone chrome accent */
    --phos:#1f8a4e; --phos-dim:#4a8f68; --amber:#a8700a; --red:#d72a1e; --blue:#1f61c9; --mag:#b3208a; --cyan:#1f61c9;
    --pass:var(--phos); --warn:var(--amber); --fail:var(--red); --unsup:var(--blue); --error:var(--mag);
    --mono:ui-monospace,"SF Mono","JetBrains Mono","Cascadia Code",Menlo,Consolas,monospace;
    --sans:"Helvetica Neue",Helvetica,Arial,system-ui,-apple-system,sans-serif;
  }
  * { box-sizing:border-box; }
  html,body { margin:0; }
  body {
    background:var(--bg);
    color:var(--ink); font-family:var(--sans); font-size:13px; line-height:1.55;
    -webkit-font-smoothing:antialiased; text-rendering:optimizeLegibility; letter-spacing:0;
    padding-bottom:5rem;
  }
  .wrap { max-width:1320px; margin:0 auto; padding:2.6rem 1.9rem 0; }

  /* ── masthead ───────────────────────────────────────────── */
  .mast { display:flex; align-items:flex-end; justify-content:space-between; gap:1.5rem; flex-wrap:wrap;
          border-bottom:1px solid var(--ink2); padding-bottom:1.05rem; }
  .brand { display:flex; flex-direction:column; gap:.4rem; }
  .kicker { font-size:.62rem; letter-spacing:.34em; color:var(--accent); text-transform:uppercase; font-weight:600; }
  .title { font-size:2.5rem; font-weight:800; letter-spacing:-.025em; line-height:.92; color:var(--ink2); }
  .title b { color:var(--accent); font-weight:800; }
  .subtitle { font-size:.7rem; color:var(--muted); letter-spacing:.13em; text-transform:uppercase; }
  .stamp { text-align:right; font-size:.62rem; color:var(--muted); letter-spacing:.08em; line-height:1.85; font-family:var(--mono); }
  .stamp .live { color:var(--phos); }
  .stamp .live::before { content:"●"; margin-right:.4em; }
  @keyframes blink { 0%,70%{opacity:1} 71%,100%{opacity:.25} }

  /* ── status bar (measurement readouts) ──────────────────── */
  .status { display:grid; grid-template-columns:repeat(auto-fit,minmax(150px,1fr)); gap:1px;
            background:var(--line); border:1px solid var(--line); margin:1.9rem 0 .4rem; }
  .led { background:var(--panel); padding:.95rem 1.1rem; position:relative; }
  .led::after { content:""; position:absolute; left:0; top:0; bottom:0; width:2px; background:var(--c,var(--phos)); }
  .led .lk { font-size:.56rem; letter-spacing:.18em; color:var(--muted); text-transform:uppercase; }
  .led .lv { font-size:1.95rem; font-weight:700; color:var(--ink2); font-family:var(--mono); font-variant-numeric:tabular-nums; line-height:1.2; letter-spacing:-.02em; }
  .led .lv small { font-size:.82rem; color:var(--muted); font-weight:400; }
  .led .lv .pct { color:var(--c,var(--phos)); }

  /* ── legend ─────────────────────────────────────────────── */
  .legend { display:flex; gap:1.6rem; flex-wrap:wrap; align-items:center; padding:1.15rem 0 .2rem; font-size:.64rem; color:var(--muted); letter-spacing:.05em; }
  .legend .chip { display:inline-flex; align-items:center; gap:.5rem; text-transform:uppercase; }
  .legend .sw { width:10px; height:10px; border-radius:0; box-shadow:none; background:var(--c); }
  .legend .note { margin-left:auto; color:var(--faint); font-style:italic; letter-spacing:.01em; text-transform:none; }

  /* ── run-launcher (cockpit only) ───────────────────────── */
  .launcher { border:1px solid var(--line); background:var(--panel); margin:1.6rem 0 .2rem; }
  .launcher .lh { display:flex; align-items:center; gap:.7rem; padding:.7rem 1.1rem; border-bottom:1px solid var(--line); background:var(--panel2); }
  .launcher .lh .t { font-size:.6rem; letter-spacing:.22em; text-transform:uppercase; color:var(--ink2); font-weight:600; }
  .launcher .lh .live { font-size:.56rem; color:var(--muted); letter-spacing:.08em; margin-left:auto; text-transform:uppercase; }
  .launcher .lh .live::before { content:"●"; color:var(--phos); margin-right:.4em; }
  .launcher .lbody { padding:.85rem 1.1rem; }
  .runbtns { display:flex; flex-wrap:wrap; gap:.5rem; }
  .runbtns button { font-family:var(--mono); font-size:.62rem; letter-spacing:.04em; text-transform:uppercase; color:var(--ink);
    background:var(--panel); border:1px solid var(--line2); padding:.42rem .75rem; cursor:pointer; transition:.12s; border-radius:0; }
  .runbtns button:hover:not(:disabled){ border-color:var(--ink2); color:var(--ink2); }
  .runbtns button:disabled{ opacity:.4; cursor:default; }
  .runbtns button .nd{ color:var(--faint); } .runbtns button.has .nd{ color:var(--phos); }
  .runconsole { margin-top:.8rem; background:var(--panel2); border:1px solid var(--line); color:var(--ink); font-family:var(--mono); font-size:.66rem; line-height:1.55;
    padding:.65rem .75rem; max-height:200px; overflow:auto; white-space:pre-wrap; display:none; }
  .runconsole.show{ display:block; }
  .runconsole .rdone{ color:var(--phos); } .runconsole .rrun{ color:var(--amber); }

  /* ── cross-protocol resilience comparison ──────────────── */
  .compare { border:1px solid var(--line); background:var(--panel); margin:1.6rem 0 .2rem; }
  .compare .cmp-head { display:flex; align-items:baseline; gap:.8rem; flex-wrap:wrap; padding:.95rem 1.1rem .7rem; border-bottom:1px solid var(--line); }
  .compare .cmp-head h2 { margin:0; font-size:1rem; font-weight:700; letter-spacing:-.01em; color:var(--ink2); }
  .compare .cmp-head .seq { font-size:.6rem; color:var(--accent); letter-spacing:.2em; }
  .compare .cmp-head .sub { font-size:.6rem; color:var(--muted); letter-spacing:.1em; text-transform:uppercase; margin-left:auto; }
  .cmp-caveat { padding:.7rem 1.1rem; color:var(--muted); font-size:.7rem; line-height:1.6; border-bottom:1px solid var(--line); }
  .cmp-caveat b { color:var(--ink2); font-weight:700; }
  .cmp-scroll { overflow-x:auto; }
  table.cmp { border-collapse:separate; border-spacing:0; width:100%; }
  table.cmp th, table.cmp td { border-right:1px solid var(--line); border-bottom:1px solid var(--line); }
  table.cmp thead th { background:var(--panel2); color:var(--muted); font-weight:600; font-size:.6rem; letter-spacing:.12em;
                       text-transform:uppercase; padding:.55rem .7rem; text-align:center; white-space:nowrap; }
  table.cmp thead th.lcol { text-align:left; }
  table.cmp thead th .axh { color:var(--faint); font-size:.52rem; display:block; letter-spacing:.08em; margin-top:.15rem; }
  table.cmp tbody th { background:var(--panel); text-align:left; padding:.5rem .9rem; white-space:nowrap; font-weight:600; font-size:.76rem; }
  table.cmp tbody th .proto { color:var(--muted); font-size:.54rem; letter-spacing:.2em; text-transform:uppercase; display:block; }
  table.cmp tbody th .lib { color:var(--ink2); }
  table.cmp tbody th .meta { color:var(--faint); font-size:.56rem; letter-spacing:.04em; margin-left:.5em; font-family:var(--mono); }
  table.cmp tbody tr.grp-top th, table.cmp tbody tr.grp-top td { border-top:1px solid var(--ink2); }
  td.cnum { text-align:center; position:relative; padding:.5rem .6rem; min-width:92px; }
  td.cnum .pct { font-size:1.15rem; font-weight:700; font-family:var(--mono); font-variant-numeric:tabular-nums; color:var(--c); line-height:1; letter-spacing:-.03em; }
  td.cnum .bar { height:2px; margin-top:.45rem; background:var(--line); position:relative; overflow:hidden; }
  td.cnum .bar i { position:absolute; left:0; top:0; bottom:0; background:var(--c); }
  td.cnum .sc { font-size:.5rem; color:var(--faint); letter-spacing:.06em; margin-top:.35rem; font-family:var(--mono); }
  td.cnum.na { color:var(--faint); } td.cnum.na .pct { color:var(--faint); font-size:.68rem; font-weight:400; }
  td.cnum.sel-able { cursor:pointer; transition:background .12s; }
  td.cnum.sel-able:hover { background:var(--panel2); }
  td.cnum.sel { box-shadow:inset 0 0 0 2px var(--c); background:var(--panel2); }
  .cmp-foot { padding:.65rem 1.1rem; color:var(--faint); font-size:.6rem; line-height:1.6; letter-spacing:.02em; }
  .cmp-foot .sch { color:var(--amber); } .cmp-foot .bid { color:var(--phos); }

  /* ── deck: grids + readout ──────────────────────────────── */
  .deck { display:grid; grid-template-columns:minmax(0,1fr) 340px; gap:1.6rem; align-items:start; margin-top:1.2rem; }
  @media (max-width:960px){ .deck{ grid-template-columns:1fr; } }

  section.cap { border:1px solid var(--line); background:var(--panel); margin-bottom:1.5rem; }
  .cap-head { display:flex; align-items:baseline; gap:.8rem; flex-wrap:wrap; padding:.95rem 1.1rem .7rem; border-bottom:1px solid var(--line); }
  .cap-head h2 { margin:0; font-size:1rem; font-weight:700; letter-spacing:-.01em; color:var(--ink2); }
  .cap-head .seq { font-size:.62rem; color:var(--accent); letter-spacing:.18em; font-family:var(--mono); }
  .tier { font-size:.55rem; letter-spacing:.14em; text-transform:uppercase; padding:.22rem .5rem; border:1px solid var(--line2); border-radius:0; white-space:nowrap; color:var(--muted); }
  .tier.t1 { color:var(--ink2); border-color:var(--ink2); }
  .tier.t2 { color:var(--muted); border-color:var(--line2); }
  .blurb { padding:.7rem 1.1rem; color:var(--muted); font-size:.74rem; line-height:1.65; border-bottom:1px solid var(--line); }

  .grid-scroll { overflow-x:auto; }
  table.grid { border-collapse:separate; border-spacing:0; width:100%; }
  table.grid th, table.grid td { padding:0; border-right:1px solid var(--line); border-bottom:1px solid var(--line); }
  table.grid thead th { background:var(--panel2); color:var(--muted); font-weight:600; font-size:.64rem; letter-spacing:.12em;
                        text-transform:uppercase; padding:.6rem .7rem; text-align:center; white-space:nowrap; position:sticky; top:0; }
  table.grid thead th.corner { text-align:left; color:var(--faint); }
  table.grid tbody th { background:var(--panel); color:var(--ink); font-weight:600; text-align:left; padding:.55rem .9rem;
                        white-space:nowrap; font-size:.78rem; letter-spacing:0; }
  td.cell { text-align:center; position:relative; }
  .v { display:flex; flex-direction:column; align-items:center; justify-content:center; gap:.22rem; width:100%; min-width:96px;
       padding:.62rem .5rem; cursor:pointer; transition:background .12s; border:1px solid transparent; }
  .v:hover { background:var(--panel2); }
  .v .dot { width:8px; height:8px; border-radius:50%; background:var(--c); }
  .v .lab { font-size:.58rem; letter-spacing:.12em; color:var(--c); font-weight:600; }
  .v.sel { background:var(--panel2); border-color:var(--c); box-shadow:inset 0 0 0 1px var(--c); }
  .v.empty { color:var(--faint); cursor:default; }
  .v.empty .dot { background:transparent; box-shadow:none; border:1px dashed var(--faint); }
  td.cell.is-fail .v .dot, td.cell.is-error .v .dot { animation:pulse 1.8s ease-in-out infinite; }
  @keyframes pulse { 0%,100%{ opacity:1; } 50%{ opacity:.35; } }

  .c-pass{ --c:var(--pass);} .c-warn{ --c:var(--warn);}
  .c-fail{ --c:var(--fail);} .c-unsup{ --c:var(--unsup);}
  .c-error{ --c:var(--error);} .c-none{ --c:var(--faint);}

  /* ── readout panel ──────────────────────────────────────── */
  .readout { position:sticky; top:1.2rem; border:1px solid var(--line); background:var(--panel); min-height:280px; }
  .ro-head { padding:.7rem .9rem; border-bottom:1px solid var(--line); display:flex; align-items:center; gap:.6rem; background:var(--panel2); }
  .ro-head .t { font-size:.6rem; letter-spacing:.24em; color:var(--muted); text-transform:uppercase; }
  .ro-head .v-pill { margin-left:auto; font-size:.6rem; letter-spacing:.1em; padding:.18rem .5rem; border-radius:0; color:var(--c); border:1px solid var(--c); }
  .ro-body { padding:.95rem; }
  .ro-empty { color:var(--faint); font-size:.74rem; line-height:1.7; padding:1.4rem .3rem; text-align:center; }
  .ro-coord { font-size:.74rem; color:var(--ink2); letter-spacing:0; margin-bottom:.2rem; font-family:var(--mono); }
  .ro-coord .lib { color:var(--ink2); font-weight:600; } .ro-coord .x { color:var(--faint); margin:0 .35em; } .ro-coord .scn { color:var(--accent); }
  .ro-sub { font-size:.62rem; color:var(--muted); letter-spacing:.04em; margin-bottom:.85rem; }
  .check { display:grid; grid-template-columns:auto 1fr; gap:.55rem; padding:.5rem 0; border-top:1px solid var(--line); }
  .check:first-child { border-top:none; }
  .check .cv { width:.5rem; align-self:start; margin-top:.35rem; height:.5rem; border-radius:50%; background:var(--c); }
  .check .cname { font-size:.72rem; color:var(--ink2); letter-spacing:0; }
  .check .cverd { font-size:.54rem; letter-spacing:.1em; color:var(--c); font-weight:600; }
  .check .cdet { grid-column:2; font-size:.7rem; color:var(--muted); line-height:1.55; margin-top:.15rem; }
  /* QoE band (delivered-% CI + A/V skew) in the readout */
  .qoe { margin-top:.7rem; padding:.55rem .65rem; border:1px solid var(--line); background:var(--panel2); }
  .qoe .qh { font-size:.54rem; letter-spacing:.14em; text-transform:uppercase; color:var(--muted); margin-bottom:.4rem; }
  .qbar { position:relative; height:8px; background:var(--panel); border:1px solid var(--line2); }
  .qbar .qci { position:absolute; top:0; bottom:0; background:var(--phos); opacity:.45; }
  .qbar .qmean { position:absolute; top:-2px; bottom:-2px; width:2px; background:var(--ink2); }
  .qsc { display:flex; justify-content:space-between; font-size:.54rem; color:var(--muted); margin-top:.25rem; font-variant-numeric:tabular-nums; font-family:var(--mono); }
  .qskew { font-size:.78rem; color:var(--c,var(--ink2)); font-variant-numeric:tabular-nums; font-family:var(--mono); }
  .qskew .qval { font-weight:700; font-size:.9rem; }
  .qoe.c-pass{ --c:var(--phos); } .qoe.c-warn{ --c:var(--amber); } .qoe.c-fail{ --c:var(--red); }
  .metrics { margin-top:.9rem; border-top:1px solid var(--line); padding-top:.7rem; }
  .metrics .mh { font-size:.56rem; letter-spacing:.18em; color:var(--faint); text-transform:uppercase; margin-bottom:.4rem; }
  .metrics .m { display:flex; justify-content:space-between; gap:1rem; font-size:.7rem; padding:.14rem 0; font-family:var(--mono); }
  .metrics .m .mk { color:var(--muted); } .metrics .m .mv { color:var(--ink2); font-variant-numeric:tabular-nums; }
  .ro-err { margin-top:.8rem; color:var(--red); font-size:.7rem; border-left:2px solid var(--red); padding-left:.6rem; }

  /* ── methodology footer ─────────────────────────────────── */
  .method { margin-top:2.6rem; border-top:1px solid var(--ink2); padding-top:1.3rem; }
  .method h3 { font-size:.62rem; letter-spacing:.24em; color:var(--accent); text-transform:uppercase; margin:0 0 .9rem; }
  .method .grid2 { display:grid; grid-template-columns:repeat(auto-fit,minmax(240px,1fr)); gap:1px; background:var(--line); border:1px solid var(--line); }
  .method .m { background:var(--panel); padding:.95rem 1.05rem; }
  .method .m b { color:var(--ink2); font-weight:700; font-size:.74rem; letter-spacing:0; }
  .method .m p { margin:.4rem 0 0; color:var(--muted); font-size:.72rem; line-height:1.65; }
  .method .m b .tag { color:var(--c,var(--accent)); }
  .colophon { margin-top:1.5rem; color:var(--faint); font-size:.62rem; letter-spacing:.04em; text-align:center; }
  .colophon b { color:var(--muted); }
  /* ── Resilience Theater (cross-protocol curves) ── */
  .thhead { display:flex; align-items:baseline; gap:.7rem; margin-bottom:.7rem; }
  .thhead .seq { color:var(--accent); font-size:.9rem; }
  .thhead h2 { margin:0; font-size:1.05rem; letter-spacing:-.01em; color:var(--ink2); font-weight:700; }
  .thhead .sub { color:var(--muted); font-size:.64rem; letter-spacing:.02em; }
  .thlegend { display:flex; flex-wrap:wrap; gap:1.1rem; margin-bottom:.5rem; }
  .thlg { display:inline-flex; align-items:center; gap:.45em; font-size:.62rem; letter-spacing:.08em; text-transform:uppercase; color:var(--muted); }
  .thlg i { width:18px; height:2px; border-radius:0; display:inline-block; }
  .thsvg { width:100%; height:auto; display:block; background:var(--panel); border:1px solid var(--line2); border-radius:0; }
  .thsvg .thgrid { stroke:var(--line); stroke-width:1; }
  .thsvg .thax { stroke:var(--line2); stroke-width:1; stroke-dasharray:2 4; }
  .thsvg .thylab { fill:var(--muted); font:600 9px var(--mono); text-anchor:end; }
  .thsvg .thxlab { fill:var(--ink2); font:600 9.5px var(--mono); text-anchor:middle; letter-spacing:.1em; }
  .thsvg .thband { opacity:.12; stroke:none; transition:opacity .12s; }
  .thsvg .thline { fill:none; stroke-width:2; opacity:.95; transition:opacity .12s,stroke-width .12s; }
  .thsvg .thdot { stroke:var(--panel); stroke-width:1.4; transition:opacity .12s; }
  .thsvg .thend { font:600 9.5px var(--mono); transition:opacity .12s; }
  .thsvg .dim { opacity:.16 !important; }
  .thsvg .thline.hot { stroke-width:3.2; opacity:1; }
  .thsvg .thband.hot { opacity:.28 !important; }
  .thsvg .thend.hot { font-weight:800; }
  .thnarr { margin-top:.9rem; padding:.75rem .95rem; border-left:2px solid var(--accent); background:var(--panel2);
            font-size:.78rem; line-height:1.7; color:var(--ink); letter-spacing:0; }
  .thnarr b { font-weight:700; } .thnarr .c-pass { color:var(--phos); } .thnarr .c-fail { color:var(--red); }
  .thfoot { margin-top:.7rem; color:var(--faint); font-size:.62rem; line-height:1.6; }
  .thempty { color:var(--muted); font-style:italic; padding:3rem 1rem; text-align:center; }
  /* ── "what survived" glass-to-glass filmstrip (per-cell baked frames) ── */
  .gtape { margin:.3rem 0 .9rem; border:1px solid var(--line); border-radius:0; overflow:hidden; background:var(--panel); }
  .gtape .gth { display:flex; align-items:center; gap:.5rem; padding:.5rem .7rem; border-bottom:1px solid var(--line); background:var(--panel2);
                font-size:.54rem; letter-spacing:.2em; text-transform:uppercase; color:var(--muted); }
  .gtape .gth .gdp { margin-left:auto; color:var(--ink2); letter-spacing:.04em; text-transform:none; font-size:.62rem; font-family:var(--mono); }
  .gmon { position:relative; aspect-ratio:16/9; max-width:540px; margin:0 auto; background:#000; overflow:hidden;
          border-bottom:1px solid var(--line); }
  .gmon img { width:100%; height:100%; object-fit:cover; display:block; transition:opacity .12s; }
  .gmon .glost { position:absolute; inset:0; display:none; flex-direction:column; align-items:center; justify-content:center; gap:.3rem;
                 background:repeating-linear-gradient(0deg,#0c0c0c,#0c0c0c 2px,#050505 2px,#050505 4px); color:var(--red);
                 font-size:.66rem; letter-spacing:.22em; text-shadow:0 0 12px rgba(255,79,79,.5); }
  .gmon .glost small { color:var(--muted); font-size:.5rem; letter-spacing:.12em; }
  .gmon.lost .glost { display:flex; } .gmon.lost img { opacity:.12; filter:grayscale(1); }
  .gmon .gtc { position:absolute; left:.5rem; bottom:.45rem; font-size:.52rem; letter-spacing:.12em; color:var(--ink2);
               background:rgba(0,0,0,.6); padding:.12rem .45rem; border-radius:2px; }
  .gstrip { display:flex; gap:2px; padding:.4rem; background:var(--panel2); }
  .gslot { position:relative; flex:1; aspect-ratio:16/9; background:#000; cursor:crosshair; overflow:hidden;
           outline:1px solid transparent; outline-offset:-1px; }
  .gslot img { width:100%; height:100%; object-fit:cover; display:block; }
  .gslot.lost { background:repeating-linear-gradient(0deg,#140707,#140707 1px,#050505 1px,#050505 3px); }
  .gslot.lost::after { content:""; position:absolute; inset:0; box-shadow:inset 0 0 0 1px rgba(215,42,30,.3); }
  .gslot.cur { outline-color:var(--accent); z-index:2; }
  .gribbon { padding:.5rem .55rem .6rem; }
  .gribbon .grow { position:relative; height:8px; border-radius:0; background:var(--line); overflow:hidden; border:1px solid var(--line2); }
  .gribbon .grow i { position:absolute; left:0; top:0; bottom:0; background:var(--phos); }
  .gribbon .glab { display:flex; justify-content:space-between; margin-top:.32rem; font-size:.56rem; letter-spacing:.04em; color:var(--muted); font-family:var(--mono); }
  .gribbon .glab .av { color:var(--phos); }
  /* grid cells with a baked filmstrip carry a marker + a hover preview that PLAYS the degradation */
  .grid td.hasfilm { position:relative; }
  .grid td.hasfilm::after { content:"▶"; position:absolute; top:3px; right:5px; font-size:.46rem; color:var(--accent); opacity:.7; }
  .gpop { position:fixed; display:none; z-index:60; width:248px; background:var(--panel); border:1px solid var(--line2); border-radius:0;
          box-shadow:0 16px 40px -18px rgba(0,0,0,.4); overflow:hidden; pointer-events:none; }
  .gpop .gpmon { position:relative; aspect-ratio:16/9; background:#000; }
  .gpop .gpmon img { width:100%; height:100%; object-fit:cover; display:block; }
  .gpop .gplost { position:absolute; inset:0; display:none; align-items:center; justify-content:center; color:var(--red);
                  font-size:.6rem; letter-spacing:.2em;
                  background:repeating-linear-gradient(0deg,#0c0c0c,#0c0c0c 2px,#050505 2px,#050505 4px); }
  .gpop .gpmon.lost .gplost { display:flex; } .gpop .gpmon.lost img { opacity:.1; filter:grayscale(1); }
  .gpop .gpbar { position:relative; height:6px; background:var(--line); } .gpop .gpbar i { position:absolute; left:0; top:0; bottom:0; background:var(--phos); }
  .gpop .gplab { padding:.34rem .55rem; font-size:.56rem; letter-spacing:.03em; color:var(--muted); display:flex; justify-content:space-between; font-family:var(--mono); }
  .gpop .gplab b { color:var(--ink2); } .gpop .gplab .av { color:var(--phos); }
</style>
</head>
<body>
<div class="wrap">
  <header class="mast">
    <div class="brand">
      <span class="kicker">Impair · Transit-WPT</span>
      <h1 class="title">SIGNAL·<b>BENCH</b></h1>
      <span class="subtitle">media-transport conformance · oracle verdict matrix</span>
    </div>
    <div class="stamp" id="stamp"></div>
  </header>

  <div class="status" id="status"></div>
  <div class="legend" id="legend"></div>

  <section class="launcher" id="launcher" style="display:none"></section>
  <section class="compare" id="compare"></section>
  <section class="theater" id="theater" style="display:none"></section>

  <div class="deck">
    <main id="sections"></main>
    <aside>
      <div class="readout" id="readout">
        <div class="ro-head"><span class="t">Readout</span></div>
        <div class="ro-body"><div class="ro-empty">Select a cell to inspect the<br>oracle's per-check verdict,<br>reasoning, and metrics.</div></div>
      </div>
    </aside>
  </div>

  <footer class="method" id="method"></footer>
  <div class="colophon">Impair / Transit-WPT · deterministic, protocol-aware impairment · graded from wire ground truth, never the implementation's self-report · <b>regenerate with the orchestrator</b></div>
</div>

<script>
(function(){
  "use strict";
  var DATA = JSON.parse(document.getElementById("tw-data").textContent);
  var V = [
    {k:"PASS",cls:"c-pass"},{k:"WARN",cls:"c-warn"},{k:"FAIL",cls:"c-fail"},
    {k:"UNSUPPORTED",cls:"c-unsup",ab:"UNSUP"},{k:"ERROR",cls:"c-error",ab:"ERR"}
  ];
  function rollup(res){
    if(res.error) return 4;
    var w=0; (res.checks||[]).forEach(function(c){ if(c.verdict>w) w=c.verdict; });
    return w;
  }
  function cellFor(m,lib,scn){
    for(var i=0;i<m.results.length;i++){ var r=m.results[i]; if(r.lib===lib&&r.scenario===scn) return r; }
    return null;
  }
  function el(tag,cls,html){ var e=document.createElement(tag); if(cls)e.className=cls; if(html!=null)e.innerHTML=html; return e; }

  // ── stamp ──
  document.getElementById("stamp").innerHTML =
    '<div class="live">BENCH ONLINE</div><div>'+(DATA.generated||"")+'</div><div>'+DATA.sections.length+' CAPABILITY MATRICES</div>';

  // ── summary ──
  var libs={}, runs=0, byV=[0,0,0,0,0], scn=0;
  DATA.sections.forEach(function(s){
    var m=s.matrix; scn+=m.scenarios.length; m.libs.forEach(function(l){libs[l]=1;});
    m.results.forEach(function(r){ runs++; byV[rollup(r)]++; });
  });
  var nLibs=Object.keys(libs).length;
  var passPct = runs? Math.round(1000*byV[0]/runs)/10 : 0;
  var status=document.getElementById("status");
  function led(k,v,c){ var e=el("div","led"); if(c)e.style.setProperty("--c",c); e.innerHTML='<div class="lk">'+k+'</div><div class="lv">'+v+'</div>'; status.appendChild(e); }
  led("Implementations", nLibs, "var(--cyan)");
  led("Capability matrices", DATA.sections.length, "var(--cyan)");
  led("Graded runs", runs, "var(--phos)");
  led("Pass rate", '<span class="pct">'+passPct+'</span><small>%</small>', "var(--phos)");
  led("Fail / warn", '<small style="color:var(--red)">'+byV[2]+'F</small> / <small style="color:var(--amber)">'+byV[1]+'W</small>', byV[2]?"var(--red)":"var(--amber)");

  // ── legend ──
  var leg=document.getElementById("legend");
  V.forEach(function(v){
    var c=el("span","chip "+v.cls); c.innerHTML='<span class="sw"></span>'+v.k; leg.appendChild(c);
  });
  leg.appendChild(el("span","note","cell colour = worst-of-checks · WARN is honest, not a failure"));

  // ── cross-protocol resilience comparison ──
  // Merges every loss-gradient capability matrix (SRT, RIST, MoQ ...) into ONE
  // normalized view: each cell is delivered-% vs that implementation's OWN clean
  // baseline (RFC-6349-style protocol-relative normalization), placed on a shared
  // CLEAN / STEADY-LOSS / BURST severity axis. Absolute throughput is never
  // compared across protocols — only the shape of each impl's resilience curve.
  function deliv(res){
    var m=res.metrics||{};
    if(typeof m.deliveredPct==="number") return m.deliveredPct; // MoQ: vs own baseline
    if(typeof m.deliveryPct==="number")  return m.deliveryPct;  // SRT/RIST: delivered/sent
    return null;
  }
  var BUCKETS=[
    {k:"CLEAN", axh:"baseline",    match:function(s){return s==="clean";}},
    {k:"STEADY LOSS", axh:"~1% / congestion", match:function(s){return s==="lte-congested"||s==="downlink-1pct";}},
    {k:"BURST", axh:"bursty / GE", match:function(s){return s==="lossy-burst"||s==="downlink-burst";}}
  ];
  function bucketScn(m,b){ for(var i=0;i<m.scenarios.length;i++){ if(b.match(m.scenarios[i])) return m.scenarios[i]; } return null; }
  // ciOf pulls a cell's bootstrap CI band (delivered-% Lo/Hi) when present.
  function ciOf(res){ var m=res.metrics||{};
    var lo=m.deliveredPctLo!=null?m.deliveredPctLo:m.deliveryPctLo;
    var hi=m.deliveredPctHi!=null?m.deliveredPctHi:m.deliveryPctHi;
    return {lo:(lo!=null?lo:null), hi:(hi!=null?hi:null)};
  }
  // compareRows reduces every loss-gradient matrix to one row per impl: its
  // delivered-% (+ CI) across the shared CLEAN / STEADY-LOSS / BURST severity axis.
  // Shared by the cross-protocol TABLE and the Resilience THEATER curves.
  function compareRows(){
    var rows=[];
    DATA.sections.forEach(function(s,si){
      var m=s.matrix;
      if(m.scenarios.indexOf("clean")<0) return;                 // not a loss-gradient matrix
      if(!BUCKETS.slice(1).some(function(b){return bucketScn(m,b);})) return;
      var proto=(s.title.split(/[ —·\/]/)[0]||s.title); // first token: SRT / RIST / MoQ
      var downlink=m.scenarios.some(function(x){return /^downlink/.test(x);});
      var t1=/tier-1/i.test(s.tier);
      m.libs.forEach(function(lib){
        if(/broken/.test(lib)) return;                            // negative-control pseudo-impl
        var clean=cellFor(m,lib,"clean"); if(!clean) return;
        var cells=BUCKETS.map(function(b){
          var scn=bucketScn(m,b); if(!scn) return null;
          var res=cellFor(m,lib,scn); if(!res) return null;
          var ci=ciOf(res);
          return {pct:deliv(res), lo:ci.lo, hi:ci.hi, verdict:rollup(res), scn:scn};
        });
        rows.push({si:si, proto:proto, lib:lib, tier:t1?"T1":"T2", downlink:downlink, cells:cells});
      });
    });
    return rows;
  }
  function buildCompare(){
    var rows=compareRows();
    var host=document.getElementById("compare");
    if(!rows.length){ host.style.display="none"; return; }
    var nProto={}; rows.forEach(function(r){nProto[r.proto]=1;});
    var h='<div class="cmp-head"><span class="seq">∑</span><h2>Cross-Protocol Resilience</h2>'+
          '<span class="sub">'+rows.length+' impls · '+Object.keys(nProto).length+' protocols · normalized</span></div>';
    h+='<div class="cmp-caveat"><b>Protocol-relative normalization (RFC 6349-style):</b> every cell is delivered-% vs that implementation’s OWN clean baseline — '+
       'absolute throughput is NEVER compared across protocols, only the <i>shape</i> of each resilience curve. '+
       '<span class="bid">SRT/RIST share a bidirectional schedule</span> (directly comparable); '+
       '<span class="sch">MoQ is downlink-only</span> (impairing the QUIC ACK path starves recovery) and each MoQ row runs at its own bitrate — read MoQ against its own baseline, not against SRT/RIST or the other MoQ row.</div>';
    h+='<div class="cmp-scroll"><table class="cmp"><thead><tr><th class="lcol">protocol ╲ severity</th>';
    BUCKETS.forEach(function(b){ h+='<th>'+b.k+'<span class="axh">'+b.axh+'</span></th>'; });
    h+='</tr></thead><tbody>';
    var lastProto=null;
    rows.forEach(function(r){
      var top=(r.proto!==lastProto); lastProto=r.proto;
      h+='<tr'+(top?' class="grp-top"':'')+'><th>'+
         (top?'<span class="proto">'+escapeHtml(r.proto)+'</span>':'')+
         '<span class="lib">'+escapeHtml(r.lib)+'</span>'+
         '<span class="meta">'+r.tier+' · '+(r.downlink?'S2C':'bi')+'</span></th>';
      r.cells.forEach(function(c){
        if(!c){ h+='<td class="cnum na"><span class="pct">—</span></td>'; return; }
        var v=V[c.verdict];
        var dattr=' data-si="'+r.si+'" data-lib="'+escapeHtml(r.lib)+'" data-scn="'+escapeHtml(c.scn)+'"';
        if(c.pct==null){ h+='<td class="cnum na sel-able '+v.cls+'"'+dattr+'><span class="pct">wire</span><div class="sc">'+escapeHtml(c.scn)+'</div></td>'; return; }
        var p=Math.max(0,Math.min(100,c.pct));
        h+='<td class="cnum sel-able '+v.cls+'"'+dattr+'><span class="pct">'+fmt(Math.round(c.pct*10)/10)+'<small style="font-size:.6rem;opacity:.6">%</small></span>'+
           '<div class="bar"><i style="width:'+p+'%"></i></div><div class="sc">'+escapeHtml(c.scn)+'</div></td>';
      });
      h+='</tr>';
    });
    h+='</tbody></table></div>';
    h+='<div class="cmp-foot">Specialized capability matrices (bonding, FEC, Tier-1 GE) are not loss-gradient comparisons and are shown only as their own sections below. '+
       '<span class="bid">bi</span> = bidirectional schedule · <span class="sch">S2C</span> = downlink-only · <b>wire</b> = graded from the wire with no self-reported delivery %.</div>';
    host.innerHTML=h;
    // Click a comparison cell to inspect the underlying run in the readout pane.
    host.addEventListener("click",function(ev){
      var td=ev.target.closest("td.sel-able"); if(!td)return;
      var m=DATA.sections[+td.dataset.si].matrix;
      var res=cellFor(m,td.dataset.lib,td.dataset.scn);
      if(res) showReadout(td.dataset.lib,td.dataset.scn,res,td,+td.dataset.si);
    });
  }
  buildCompare();

  // ── Resilience Theater: the cross-protocol story as overlaid curves ──
  // Each impl's delivered-% plotted across CLEAN → STEADY → BURST, normalized to
  // its own clean baseline, family-coloured (ARQ greens hold the line; MoQ cyan
  // collapses at BURST) with bootstrap-CI ribbons. The hero read of the moat.
  var THEFAM={SRT:"#1f8a4e", RIST:"#0d8a7a", MoQ:"#1f61c9", "2022-7":"#a8700a", "2022-1":"#a8700a"};
  function theFam(p){ return THEFAM[p]||"#9bb0a6"; }
  function buildTheater(){
    var host=document.getElementById("theater"); if(!host) return;
    var rows=compareRows().filter(function(r){ return r.cells.length===3 && r.cells.every(function(c){return c&&c.pct!=null;}); });
    if(!rows.length){ host.innerHTML='<div class="thempty">No loss-gradient matrices loaded — run the SRT / RIST / MoQ matrices to populate the resilience curves.</div>'; return; }
    var W=980,H=460,ml=52,mr=196,mt=26,mb=58,pw=W-ml-mr,ph=H-mt-mb;
    var xs=[ml, ml+pw/2, ml+pw];
    function yOf(p){ return mt+ph*(1-Math.max(0,Math.min(100,p))/100); }
    var s='<svg viewBox="0 0 '+W+' '+H+'" class="thsvg" preserveAspectRatio="xMidYMid meet">';
    [0,25,50,75,100].forEach(function(g){ var y=yOf(g); s+='<line class="thgrid" x1="'+ml+'" y1="'+y+'" x2="'+(ml+pw)+'" y2="'+y+'"/>'+
      '<text class="thylab" x="'+(ml-9)+'" y="'+(y+3)+'">'+g+'</text>'; });
    ["CLEAN","STEADY LOSS","BURST"].forEach(function(lab,i){
      s+='<line class="thax" x1="'+xs[i]+'" y1="'+mt+'" x2="'+xs[i]+'" y2="'+(mt+ph)+'"/>'+
         '<text class="thxlab" x="'+xs[i]+'" y="'+(H-mb+24)+'">'+lab+'</text>'; });
    // CI ribbons first (under the lines)
    rows.forEach(function(r,ri){
      if(!r.cells.some(function(c){return c.lo!=null&&c.hi!=null;})) return;
      var up="",dn="";
      r.cells.forEach(function(c,i){ up+=xs[i]+","+yOf(c.hi!=null?c.hi:c.pct)+" "; });
      for(var i=2;i>=0;i--){ var c=r.cells[i]; dn+=xs[i]+","+yOf(c.lo!=null?c.lo:c.pct)+" "; }
      s+='<polygon class="thband" data-row="'+ri+'" points="'+up+dn+'" style="fill:'+theFam(r.proto)+'"/>';
    });
    // lines + dots + end labels
    var ends=[];
    rows.forEach(function(r,ri){
      var col=theFam(r.proto), pts=r.cells.map(function(c,i){return xs[i]+","+yOf(c.pct);});
      s+='<polyline class="thline" data-row="'+ri+'" points="'+pts.join(" ")+'" style="stroke:'+col+'"/>';
      r.cells.forEach(function(c,i){ s+='<circle class="thdot" data-row="'+ri+'" cx="'+xs[i]+'" cy="'+yOf(c.pct)+'" r="3.4" style="fill:'+col+'"><title>'+escapeHtml(r.lib)+' · '+escapeHtml(c.scn)+' · '+fmt(Math.round(c.pct*10)/10)+'%</title></circle>'; });
      ends.push({y:yOf(r.cells[2].pct), col:col, lib:r.lib, pct:r.cells[2].pct, ri:ri});
    });
    // de-overlap end labels (greedy push-down)
    ends.sort(function(a,b){return a.y-b.y;});
    for(var i=1;i<ends.length;i++){ if(ends[i].y-ends[i-1].y<13) ends[i].y=ends[i-1].y+13; }
    ends.forEach(function(e){ s+='<text class="thend" data-row="'+e.ri+'" x="'+(ml+pw+10)+'" y="'+(e.y+3)+'" style="fill:'+e.col+'">'+escapeHtml(e.lib)+' '+fmt(Math.round(e.pct))+'%</text>'; });
    s+='</svg>';
    // the story line: ARQ vs MoQ at BURST
    var arq=rows.filter(function(r){return r.proto==="SRT"||r.proto==="RIST";}).map(function(r){return r.cells[2].pct;});
    var moq=rows.filter(function(r){return r.proto==="MoQ";}).map(function(r){return r.cells[2].pct;});
    function avg(a){return a.length?a.reduce(function(x,y){return x+y;},0)/a.length:null;}
    var narr="";
    if(arq.length&&moq.length){
      narr='Under <b>BURST</b> loss, ARQ (SRT/RIST) holds <b class="c-pass">~'+Math.round(avg(arq))+'%</b> of its own baseline — the retransmit rides out the loss runs — while MoQ-over-QUIC collapses to <b class="c-fail">~'+Math.round(avg(moq))+'%</b> (head-of-line blocking, no recovery in the live window). Steady loss both recover; the burst is the knee.';
    }
    var fams={}; rows.forEach(function(r){fams[r.proto]=1;});
    var legend=Object.keys(fams).map(function(p){return '<span class="thlg"><i style="background:'+theFam(p)+'"></i>'+escapeHtml(p)+'</span>';}).join("");
    host.innerHTML='<div class="thhead"><span class="seq">∑</span><h2>Resilience Theater</h2>'+
      '<span class="sub">'+rows.length+' impls · delivered % vs severity · each normalized to its own clean baseline</span></div>'+
      '<div class="thlegend">'+legend+'</div>'+s+
      (narr?'<div class="thnarr">'+narr+'</div>':'')+
      '<div class="thfoot">Y axis is each implementation’s delivered-% against its OWN clean baseline (RFC-6349-style) — the <i>shape</i> of resilience, never absolute throughput across protocols. CI ribbons are bootstrap bands where reps exist.</div>';
    // hover a line/label to spotlight it
    function spot(ri){ host.querySelectorAll(".thline,.thband,.thdot,.thend").forEach(function(el){
      var on=ri==null||+el.dataset.row===ri; el.classList.toggle("dim",ri!=null&&!on); el.classList.toggle("hot",ri!=null&&on); }); }
    host.querySelectorAll(".thline,.thend").forEach(function(el){
      el.addEventListener("mouseenter",function(){spot(+el.dataset.row);});
      el.addEventListener("mouseleave",function(){spot(null);});
    });
  }
  buildTheater();

  // ── run-launcher (only when served by the cockpit; static file has no API) ──
  function buildLauncher(){
    fetch("/api/run/targets").then(function(r){return r.ok?r.json():Promise.reject();}).then(function(targets){
      var host=document.getElementById("launcher"); host.style.display="";
      host.innerHTML='<div class="lh"><span class="t">Run launcher</span><span class="live">cockpit</span></div>'+
        '<div class="lbody"><div class="runbtns" id="runbtns"></div><div class="runconsole" id="runconsole"></div></div>';
      var btns=document.getElementById("runbtns");
      targets.forEach(function(t){
        var b=document.createElement("button"); if(t.hasData)b.className="has";
        b.innerHTML='▶ '+escapeHtml(t.label)+' <span class="nd">'+(t.hasData?"●":"○")+'</span>';
        b.onclick=function(){ startRun(t.key); };
        btns.appendChild(b);
      });
    }).catch(function(){ /* static file (no cockpit API): leave launcher hidden */ });
  }
  function startRun(key){
    document.querySelectorAll("#runbtns button").forEach(function(b){ b.disabled=true; });
    var con=document.getElementById("runconsole"); con.className="runconsole show"; con.textContent="starting "+key+" …";
    fetch("/api/run",{method:"POST",body:JSON.stringify({target:key})}).then(function(r){
      if(!r.ok) return r.text().then(function(t){ throw t; });
      pollRun();
    }).catch(function(e){ con.textContent="error: "+e; document.querySelectorAll("#runbtns button").forEach(function(b){ b.disabled=false; }); });
  }
  function pollRun(){
    fetch("/api/run/status").then(function(r){return r.json();}).then(function(s){
      var con=document.getElementById("runconsole");
      con.textContent=(s.log||"")+(s.running?"\n[ running "+s.elapsed+"s ]":"");
      con.scrollTop=con.scrollHeight;
      if(s.done && !s.running){
        con.textContent+="\n— reloading dashboard with fresh results —";
        setTimeout(function(){ location.reload(); }, 900);
      } else { setTimeout(pollRun, 700); }
    }).catch(function(){ setTimeout(pollRun, 1500); });
  }
  buildLauncher();

  // ── readout ──
  var ro=document.getElementById("readout");
  var selected=null;
  function fmt(n){ if(n===Math.round(n)) return String(n); return (Math.round(n*1000)/1000).toString(); }
  // qoeBand renders a delivered-% bootstrap-CI band [Lo, mean, Hi] when present
  // (MoQ deliveredPct* / SRT-RIST deliveryPct*), plus an A/V-skew sync strip for
  // the MoQ rows — the glass-to-glass QoE the matrix grades, made legible.
  function qoeBand(m){
    var mean=m.deliveredPctMean!=null?m.deliveredPctMean:m.deliveryPctMean;
    var lo  =m.deliveredPctLo  !=null?m.deliveredPctLo  :m.deliveryPctLo;
    var hi  =m.deliveredPctHi  !=null?m.deliveredPctHi  :m.deliveryPctHi;
    var pt  =m.deliveredPct    !=null?m.deliveredPct    :m.deliveryPct;
    var h="";
    if(lo!=null&&hi!=null){
      var clamp=function(x){return Math.max(0,Math.min(100,x));};
      var l=clamp(lo),hh=clamp(hi),mn=clamp(mean!=null?mean:pt);
      h+='<div class="qoe"><div class="qh">delivered % · bootstrap CI'+(m.reps?' ('+fmt(m.reps)+' reps)':'')+'</div>'+
         '<div class="qbar"><span class="qci" style="left:'+l+'%;width:'+Math.max(1,hh-l)+'%"></span>'+
         '<span class="qmean" style="left:'+mn+'%"></span></div>'+
         '<div class="qsc"><span>'+fmt(Math.round(l))+'%</span><span>'+fmt(Math.round(mn))+'% mean</span><span>'+fmt(Math.round(hh))+'%</span></div></div>';
    } else if(pt!=null){
      var pc=Math.max(0,Math.min(100,pt));
      h+='<div class="qoe"><div class="qh">delivered %</div><div class="qbar"><span class="qci" style="left:0;width:'+pc+'%;opacity:.5"></span><span class="qmean" style="left:'+pc+'%"></span></div></div>';
    }
    if(typeof m.avSkewMs==="number"){
      var sk=Math.abs(m.avSkewMs), cls=sk<=400?"c-pass":sk<=1500?"c-warn":"c-fail";
      // large skews read as seconds (a 104728 ms desync is "104.7 s", not a wall of ms)
      var sg=m.avSkewMs<0?"-":"+", val=sk>=1000?sg+fmt(Math.round(sk/100)/10)+" s":sg+fmt(Math.round(sk))+" ms";
      h+='<div class="qoe '+cls+'"><div class="qh">A/V sync skew</div><div class="qskew"><span class="qval">'+val+'</span> · '+
         (sk<=400?"in sync":sk<=1500?"drifting":"DESYNCED — picture vs sound")+'</div></div>';
    }
    return h;
  }
  // framesFor returns a cell's baked "what survived" filmstrip data (or null).
  function framesFor(si,lib,scn){ var s=DATA.sections[si]; if(!s||!s.frames)return null; var l=s.frames[lib]; return l?(l[scn]||null):null; }
  // whatSurvived renders the glass-to-glass filmstrip as a TIMELINE: live thumbs
  // for the delivered fraction, "signal lost" slots for the rest, with the
  // audio-survival ribbon running underneath — so a collapse reads as the picture
  // going dark while audio (the green ribbon) continues past the last good frame.
  function whatSurvived(fr){
    var thumbs=fr.thumbs||[]; if(!thumbs.length) return "";
    var N=8, cl=function(x){return Math.max(0,Math.min(100,x==null?100:x));};
    var dp=cl(fr.deliveredPct), ap=cl(fr.audioPct);
    var live=Math.max(1,Math.min(N,Math.round(N*dp/100)));
    var slots=[];
    for(var i=0;i<N;i++){
      if(i<live) slots.push({src:thumbs[Math.min(thumbs.length-1,Math.floor(i*thumbs.length/live))]});
      else slots.push({lost:true});
    }
    var lastSrc=slots[live-1]&&slots[live-1].src;
    var h='<div class="gtape">';
    h+='<div class="gth">What survived · glass-to-glass<span class="gdp">'+fmt(Math.round(dp))+'% picture'+(fr.keyframes!=null?' · '+fmt(fr.keyframes)+' keyframes':'')+'</span></div>';
    h+='<div class="gmon'+(lastSrc?'':' lost')+'"><img src="'+(lastSrc||'')+'" alt=""><div class="glost">▣ SIGNAL LOST<small>picture gone · audio continues</small></div><div class="gtc">last good frame</div></div>';
    h+='<div class="gstrip">';
    slots.forEach(function(s,i){
      if(s.lost) h+='<div class="gslot lost" data-i="'+i+'"></div>';
      else h+='<div class="gslot'+(i===live-1?' cur':'')+'" data-i="'+i+'" data-src="'+s.src+'"><img src="'+s.src+'" alt=""></div>';
    });
    h+='</div>';
    h+='<div class="gribbon"><div class="grow"><i style="width:'+ap+'%"></i></div>'+
       '<div class="glab"><span>video '+fmt(Math.round(dp))+'%</span><span class="av">audio '+fmt(Math.round(ap))+'% — survives past the last frame</span></div></div>';
    h+='</div>';
    return h;
  }
  // ── grid hover preview: PLAY the "what survived" degradation in a floating card ──
  var gpop=null, gpopTimer=null;
  function ensureGpop(){
    if(gpop) return gpop;
    gpop=document.createElement("div"); gpop.className="gpop";
    gpop.innerHTML='<div class="gpmon"><img alt=""><div class="gplost">▣ SIGNAL LOST</div></div>'+
      '<div class="gpbar"><i></i></div><div class="gplab"><span class="vd"></span><span class="av"></span></div>';
    document.body.appendChild(gpop);
    return gpop;
  }
  function gpopShow(fr,td){
    var thumbs=fr.thumbs||[]; if(!thumbs.length) return;
    var p=ensureGpop(), cl=function(x){return Math.max(0,Math.min(100,x==null?100:x));};
    var dp=cl(fr.deliveredPct), ap=cl(fr.audioPct), N=8, live=Math.max(1,Math.min(N,Math.round(N*dp/100)));
    var seq=[]; for(var i=0;i<N;i++){ seq.push(i<live?thumbs[Math.min(thumbs.length-1,Math.floor(i*thumbs.length/live))]:null); }
    var mon=p.querySelector(".gpmon"), img=p.querySelector("img");
    p.querySelector(".gpbar i").style.width=ap+"%";
    p.querySelector(".gplab .vd").innerHTML='<b>'+fmt(Math.round(dp))+'%</b> picture';
    p.querySelector(".gplab .av").textContent='audio '+fmt(Math.round(ap))+'%';
    var k=0; function step(){ var s=seq[k%seq.length]; if(s){ mon.classList.remove("lost"); img.src=s; } else mon.classList.add("lost"); k++; }
    step(); clearInterval(gpopTimer); gpopTimer=setInterval(step,300);
    p.style.display="block";
    var r=td.getBoundingClientRect(), pw=248, ph=p.offsetHeight||168;
    var left=Math.min(window.innerWidth-pw-8,Math.max(8,r.left-20)), top=r.top-ph-10;
    if(top<8) top=r.bottom+10;
    p.style.left=left+"px"; p.style.top=top+"px";
  }
  function gpopHide(){ if(gpopTimer){clearInterval(gpopTimer);gpopTimer=null;} if(gpop)gpop.style.display="none"; }

  function showReadout(lib,scn,res,td,si){
    if(selected) selected.classList.remove("sel");
    selected = (td&&td.querySelector(".v")) || td; if(selected) selected.classList.add("sel");
    var rv=rollup(res), v=V[rv];
    var h='<div class="ro-head"><span class="t">Readout</span><span class="v-pill '+v.cls+'">'+v.k+'</span></div><div class="ro-body">';
    h+='<div class="ro-coord"><span class="lib">'+lib+'</span><span class="x">▸</span><span class="scn">'+scn+'</span></div>';
    h+='<div class="ro-sub">'+(res.checks?res.checks.length:0)+' invariant'+((res.checks&&res.checks.length===1)?"":"s")+' · rollup = '+v.k+'</div>';
    h+=qoeBand(res.metrics||{});
    var fr=(si!=null)?framesFor(si,lib,scn):null; if(fr) h+=whatSurvived(fr);
    (res.checks||[]).forEach(function(c){
      var cv=V[c.verdict];
      h+='<div class="check '+cv.cls+'"><span class="cv"></span><div><span class="cname">'+c.name+'</span> <span class="cverd">'+(cv.ab||cv.k)+'</span>'+
         (c.detail?'<div class="cdet">'+escapeHtml(c.detail)+'</div>':'')+'</div></div>';
    });
    var mk=Object.keys(res.metrics||{});
    if(mk.length){
      h+='<div class="metrics"><div class="mh">Metrics</div>';
      mk.forEach(function(k){ h+='<div class="m"><span class="mk">'+k+'</span><span class="mv">'+fmt(res.metrics[k])+'</span></div>'; });
      h+='</div>';
    }
    if(res.error) h+='<div class="ro-err">ERROR · '+escapeHtml(res.error)+'</div>';
    h+='</div>';
    ro.innerHTML=h;
    // Scrub the filmstrip: hovering a slot drives the program monitor; lost slots
    // black it out (the freeze/desync made tactile).
    var tape=ro.querySelector(".gtape");
    if(tape){
      var mon=tape.querySelector(".gmon"), monImg=mon.querySelector("img"), tc=mon.querySelector(".gtc"), slots=tape.querySelectorAll(".gslot");
      slots.forEach(function(sl){
        sl.addEventListener("mouseenter",function(){
          slots.forEach(function(x){x.classList.remove("cur");}); sl.classList.add("cur");
          if(sl.classList.contains("lost")){ mon.classList.add("lost"); tc.textContent="signal lost · "+(+sl.dataset.i+1)+"/"+slots.length; }
          else { mon.classList.remove("lost"); monImg.src=sl.dataset.src; tc.textContent="frame "+(+sl.dataset.i+1)+"/"+slots.length; }
        });
      });
    }
    if(window.matchMedia("(max-width:960px)").matches) ro.scrollIntoView({behavior:"smooth",block:"nearest"});
  }
  function escapeHtml(s){ return String(s).replace(/[&<>"]/g,function(c){return {"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;"}[c];}); }

  // ── sections + grids ──
  var host=document.getElementById("sections");
  DATA.sections.forEach(function(s,si){
    var m=s.matrix, t1=/tier-1/i.test(s.tier);
    var sec=el("section","cap");
    var head=el("div","cap-head");
    head.innerHTML='<span class="seq">'+String(si+1).padStart(2,"0")+'</span><h2>'+escapeHtml(s.title)+'</h2>'+
      '<span class="tier '+(t1?"t1":"t2")+'">'+escapeHtml(s.tier)+'</span>';
    sec.appendChild(head);
    if(s.blurb) sec.appendChild(el("div","blurb",escapeHtml(s.blurb)));

    var scroll=el("div","grid-scroll");
    var tbl=el("table","grid");
    var thead='<thead><tr><th class="corner">scenario ╲ impl</th>';
    m.libs.forEach(function(l){ thead+='<th>'+escapeHtml(l)+'</th>'; });
    thead+='</tr></thead>';
    var tb=el("tbody");
    m.scenarios.forEach(function(scn,ri){
      var tr=el("tr");
      tr.appendChild(el("th",null,escapeHtml(scn)));
      m.libs.forEach(function(lib){
        var res=cellFor(m,lib,scn), td=el("td","cell");
        if(!res){ td.innerHTML='<div class="v empty c-none"><span class="dot"></span><span class="lab">—</span></div>'; tr.appendChild(td); return; }
        var rv=rollup(res), v=V[rv];
        if(rv===2) td.classList.add("is-fail"); if(rv===4) td.classList.add("is-error");
        var vd=el("div","v "+v.cls);
        vd.innerHTML='<span class="dot"></span><span class="lab">'+(v.ab||v.k)+'</span>';
        vd.style.animationDelay=(ri*30+0)+"ms";
        td.appendChild(vd);
        td.addEventListener("click",function(){ showReadout(lib,scn,res,td,si); });
        var fr=framesFor(si,lib,scn);
        if(fr&&fr.thumbs&&fr.thumbs.length){ // glass-to-glass cell — hover plays what survived
          td.classList.add("hasfilm");
          td.addEventListener("mouseenter",function(){ gpopShow(fr,td); });
          td.addEventListener("mouseleave",gpopHide);
        }
        tr.appendChild(td);
      });
      tb.appendChild(tr);
    });
    tbl.innerHTML=thead;
    tbl.appendChild(tb);
    scroll.appendChild(tbl);
    sec.appendChild(scroll);
    host.appendChild(sec);
  });

  // ── methodology footer ──
  var method=document.getElementById("method");
  var cards=[
    ["Worst-of-checks","c-fail","A run is graded PASS only if every invariant passes; the cell takes the colour of its worst check. This is the AND-gated KPI tuple of ITU-T Y.1564."],
    ["WARN is honest","c-warn","WARN marks a legal-but-odd outcome the wire can't fully attribute — e.g. a bonded double-loss with no exact per-link ledger. It is permitted, not a failure."],
    ["Wire ground truth","c-pass","Verdicts come from a passive protocol observer at the relay, never the implementation's self-reported stats. Opaque (black-box) SUTs are graded from the wire alone."],
    ["Tier-1 vs Tier-2","c-unsup","Tier-1 is bit-deterministic (virtual clock, no sockets) — byte-identical run to run. Tier-2 drives real binaries on real sockets; the impairment schedule is deterministic, the outcomes distribution-reproducible."]
  ];
  var h='<h3>How a verdict is computed</h3><div class="grid2">';
  cards.forEach(function(c){ h+='<div class="m '+c[1]+'"><b><span class="tag">▪</span> '+c[0]+'</b><p>'+c[2]+'</p></div>'; });
  h+='</div>';
  method.innerHTML=h;
})();
</script>
</body>
</html>
`
