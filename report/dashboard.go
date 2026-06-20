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
  :root {
    --bg:#06080a; --bg2:#080b0e; --panel:#0c1014; --panel2:#10151b; --line:#1a232c;
    --ink:#c6d6cb; --ink2:#e9f2ec; --muted:#5f7268; --faint:#37463e;
    --phos:#2fe08a; --phos-dim:#1f9a60; --amber:#f1a52f; --red:#ff4f57; --blue:#5e7bd6; --mag:#ff3d80;
    --cyan:#36cfe0;
    --pass:var(--phos); --warn:var(--amber); --fail:var(--red); --unsup:var(--blue); --error:var(--mag);
    --mono:ui-monospace,"SF Mono","JetBrains Mono","Cascadia Code","Source Code Pro",Menlo,Consolas,monospace;
  }
  * { box-sizing:border-box; }
  html,body { margin:0; }
  body {
    background:
      radial-gradient(120% 80% at 50% -10%, rgba(47,224,138,.06), transparent 60%),
      radial-gradient(140% 90% at 100% 0%, rgba(54,207,224,.045), transparent 55%),
      var(--bg);
    color:var(--ink); font-family:var(--mono); font-size:13px; line-height:1.5;
    -webkit-font-smoothing:antialiased; letter-spacing:.01em;
    padding-bottom:5rem;
  }
  /* CRT scanlines + faint grid, layered over everything but pointer-transparent */
  body::before {
    content:""; position:fixed; inset:0; pointer-events:none; z-index:9999; opacity:.5;
    background:repeating-linear-gradient(0deg, rgba(0,0,0,0) 0, rgba(0,0,0,0) 2px, rgba(0,0,0,.18) 3px, rgba(0,0,0,0) 4px);
    mix-blend-mode:multiply;
  }
  body::after {
    content:""; position:fixed; inset:0; pointer-events:none; z-index:-1;
    background:
      linear-gradient(var(--line) 1px, transparent 1px) 0 0/64px 64px,
      linear-gradient(90deg, var(--line) 1px, transparent 1px) 0 0/64px 64px;
    opacity:.18; mask-image:radial-gradient(120% 120% at 50% 0%, #000 30%, transparent 80%);
  }
  .wrap { max-width:1320px; margin:0 auto; padding:2.4rem 1.6rem 0; }

  /* ── masthead ───────────────────────────────────────────── */
  .mast { display:flex; align-items:flex-end; justify-content:space-between; gap:1.5rem; flex-wrap:wrap;
          border-bottom:1px solid var(--line); padding-bottom:1.1rem; }
  .brand { display:flex; flex-direction:column; gap:.35rem; }
  .kicker { font-size:.66rem; letter-spacing:.42em; color:var(--cyan); text-transform:uppercase; }
  .title { font-size:2.5rem; font-weight:700; letter-spacing:.02em; line-height:.95; color:var(--ink2);
           text-shadow:0 0 18px rgba(47,224,138,.32), 0 0 2px rgba(47,224,138,.6); }
  .title b { color:var(--phos); }
  .subtitle { font-size:.74rem; color:var(--muted); letter-spacing:.12em; text-transform:uppercase; }
  .stamp { text-align:right; font-size:.66rem; color:var(--muted); letter-spacing:.14em; line-height:1.7; }
  .stamp .live { color:var(--phos); }
  .stamp .live::before { content:"●"; margin-right:.4em; animation:blink 2.4s steps(1) infinite; }
  @keyframes blink { 0%,70%{opacity:1} 71%,100%{opacity:.25} }

  /* ── status bar (LED readouts) ──────────────────────────── */
  .status { display:grid; grid-template-columns:repeat(auto-fit,minmax(150px,1fr)); gap:1px;
            background:var(--line); border:1px solid var(--line); margin:1.4rem 0 .4rem; }
  .led { background:linear-gradient(180deg,var(--panel2),var(--panel)); padding:.85rem 1rem; position:relative; overflow:hidden; }
  .led::after { content:""; position:absolute; left:0; top:0; bottom:0; width:2px; background:var(--c,var(--phos)); box-shadow:0 0 10px var(--c,var(--phos)); }
  .led .lk { font-size:.6rem; letter-spacing:.2em; color:var(--muted); text-transform:uppercase; }
  .led .lv { font-size:1.9rem; font-weight:700; color:var(--ink2); font-variant-numeric:tabular-nums; line-height:1.15; }
  .led .lv small { font-size:.9rem; color:var(--muted); font-weight:400; }
  .led .lv .pct { color:var(--c,var(--phos)); text-shadow:0 0 12px var(--c,var(--phos)); }

  /* ── legend ─────────────────────────────────────────────── */
  .legend { display:flex; gap:1.4rem; flex-wrap:wrap; align-items:center; padding:1rem 0 .2rem; font-size:.68rem; color:var(--muted); letter-spacing:.08em; }
  .legend .chip { display:inline-flex; align-items:center; gap:.45rem; text-transform:uppercase; }
  .legend .sw { width:11px; height:11px; border-radius:2px; box-shadow:0 0 8px var(--c); background:var(--c); }
  .legend .note { margin-left:auto; color:var(--faint); font-style:italic; letter-spacing:.04em; text-transform:none; }

  /* ── cross-protocol resilience comparison ──────────────── */
  .compare { border:1px solid var(--line); background:linear-gradient(180deg,rgba(54,207,224,.04),transparent); margin:1.4rem 0 .2rem; }
  .compare .cmp-head { display:flex; align-items:baseline; gap:.8rem; flex-wrap:wrap; padding:.95rem 1.1rem .7rem; border-bottom:1px solid var(--line);
                       background:repeating-linear-gradient(135deg, rgba(94,123,214,.04) 0 8px, transparent 8px 16px); }
  .compare .cmp-head h2 { margin:0; font-size:1.02rem; font-weight:700; letter-spacing:.04em; color:var(--ink2); }
  .compare .cmp-head .seq { font-size:.62rem; color:var(--faint); letter-spacing:.2em; }
  .compare .cmp-head .sub { font-size:.62rem; color:var(--muted); letter-spacing:.1em; text-transform:uppercase; margin-left:auto; }
  .cmp-caveat { padding:.7rem 1.1rem; color:var(--muted); font-size:.7rem; line-height:1.6; border-bottom:1px solid var(--line); }
  .cmp-caveat b { color:var(--cyan); font-weight:700; }
  .cmp-scroll { overflow-x:auto; }
  table.cmp { border-collapse:separate; border-spacing:0; width:100%; }
  table.cmp th, table.cmp td { border-right:1px solid var(--line); border-bottom:1px solid var(--line); }
  table.cmp thead th { background:var(--panel); color:var(--muted); font-weight:600; font-size:.62rem; letter-spacing:.12em;
                       text-transform:uppercase; padding:.55rem .7rem; text-align:center; white-space:nowrap; }
  table.cmp thead th.lcol { text-align:left; }
  table.cmp thead th .axh { color:var(--faint); font-size:.54rem; display:block; letter-spacing:.1em; margin-top:.15rem; }
  table.cmp tbody th { background:var(--panel); text-align:left; padding:.5rem .9rem; white-space:nowrap; font-weight:600; font-size:.76rem; }
  table.cmp tbody th .proto { color:var(--c,var(--cyan)); font-size:.56rem; letter-spacing:.2em; text-transform:uppercase; display:block; }
  table.cmp tbody th .lib { color:var(--ink2); }
  table.cmp tbody th .meta { color:var(--faint); font-size:.58rem; letter-spacing:.06em; margin-left:.5em; }
  table.cmp tbody tr.grp-top th, table.cmp tbody tr.grp-top td { border-top:2px solid var(--faint); }
  td.cnum { text-align:center; position:relative; padding:.5rem .6rem; min-width:92px; }
  td.cnum .pct { font-size:1.15rem; font-weight:700; font-variant-numeric:tabular-nums; color:var(--c); text-shadow:0 0 12px var(--cg,transparent); line-height:1; }
  td.cnum .bar { height:3px; margin-top:.4rem; background:var(--faint); position:relative; overflow:hidden; }
  td.cnum .bar i { position:absolute; left:0; top:0; bottom:0; background:var(--c); box-shadow:0 0 8px var(--c); }
  td.cnum .sc { font-size:.52rem; color:var(--faint); letter-spacing:.08em; margin-top:.3rem; }
  td.cnum.na { color:var(--faint); } td.cnum.na .pct { color:var(--faint); font-size:.7rem; font-weight:400; text-shadow:none; }
  td.cnum.sel-able { cursor:pointer; transition:background .12s, box-shadow .12s; }
  td.cnum.sel-able:hover { background:rgba(255,255,255,.03); }
  td.cnum.sel { box-shadow:inset 0 0 0 1px var(--c), inset 0 0 16px -6px var(--c); background:rgba(255,255,255,.02); }
  .cmp-foot { padding:.65rem 1.1rem; color:var(--faint); font-size:.62rem; line-height:1.6; letter-spacing:.04em; }
  .cmp-foot .sch { color:var(--amber); } .cmp-foot .bid { color:var(--phos-dim); }

  /* ── deck: grids + readout ──────────────────────────────── */
  .deck { display:grid; grid-template-columns:minmax(0,1fr) 340px; gap:1.6rem; align-items:start; margin-top:1.2rem; }
  @media (max-width:960px){ .deck{ grid-template-columns:1fr; } }

  section.cap { border:1px solid var(--line); background:linear-gradient(180deg,var(--bg2),transparent); margin-bottom:1.4rem; }
  .cap-head { display:flex; align-items:baseline; gap:.8rem; flex-wrap:wrap; padding:.95rem 1.1rem .7rem; border-bottom:1px solid var(--line);
              background:repeating-linear-gradient(135deg, rgba(54,207,224,.03) 0 8px, transparent 8px 16px); }
  .cap-head h2 { margin:0; font-size:1.02rem; font-weight:700; letter-spacing:.04em; color:var(--ink2); }
  .cap-head .seq { font-size:.62rem; color:var(--faint); letter-spacing:.2em; }
  .tier { font-size:.58rem; letter-spacing:.16em; text-transform:uppercase; padding:.22rem .5rem; border:1px solid; border-radius:2px; white-space:nowrap; }
  .tier.t1 { color:var(--phos); border-color:var(--phos-dim); background:rgba(47,224,138,.07); box-shadow:inset 0 0 12px rgba(47,224,138,.08); }
  .tier.t2 { color:var(--cyan); border-color:#1c4a52; background:rgba(54,207,224,.06); }
  .blurb { padding:.7rem 1.1rem; color:var(--muted); font-size:.74rem; line-height:1.6; border-bottom:1px solid var(--line); }

  .grid-scroll { overflow-x:auto; }
  table.grid { border-collapse:separate; border-spacing:0; width:100%; }
  table.grid th, table.grid td { padding:0; border-right:1px solid var(--line); border-bottom:1px solid var(--line); }
  table.grid thead th { background:var(--panel); color:var(--muted); font-weight:600; font-size:.66rem; letter-spacing:.12em;
                        text-transform:uppercase; padding:.6rem .7rem; text-align:center; white-space:nowrap; position:sticky; top:0; }
  table.grid thead th.corner { text-align:left; color:var(--faint); }
  table.grid tbody th { background:var(--panel); color:var(--ink); font-weight:600; text-align:left; padding:.55rem .9rem;
                        white-space:nowrap; font-size:.78rem; letter-spacing:.02em; }
  td.cell { text-align:center; position:relative; }
  .v { display:flex; flex-direction:column; align-items:center; justify-content:center; gap:.18rem; width:100%; min-width:96px;
       padding:.6rem .5rem; cursor:pointer; transition:background .12s, box-shadow .12s, transform .12s; border:1px solid transparent; }
  .v:hover { background:rgba(255,255,255,.025); transform:translateY(-1px); }
  .v .dot { width:9px; height:9px; border-radius:50%; background:var(--c); box-shadow:0 0 10px var(--c),0 0 2px var(--c); }
  .v .lab { font-size:.6rem; letter-spacing:.13em; color:var(--c); text-shadow:0 0 9px var(--cg,transparent); }
  .v.sel { background:rgba(47,224,138,.06); border-color:var(--c); box-shadow:inset 0 0 0 1px var(--c), 0 0 16px -4px var(--c); }
  .v.empty { color:var(--faint); cursor:default; }
  .v.empty .dot { background:transparent; box-shadow:none; border:1px dashed var(--faint); }
  td.cell.is-fail .v .dot, td.cell.is-error .v .dot { animation:pulse 1.6s ease-in-out infinite; }
  @keyframes pulse { 0%,100%{ box-shadow:0 0 10px var(--c),0 0 2px var(--c);} 50%{ box-shadow:0 0 18px var(--c),0 0 5px var(--c);} }

  .c-pass{ --c:var(--pass); --cg:rgba(47,224,138,.55);} .c-warn{ --c:var(--warn); --cg:rgba(241,165,47,.5);}
  .c-fail{ --c:var(--fail); --cg:rgba(255,79,87,.5);} .c-unsup{ --c:var(--unsup); --cg:rgba(94,123,214,.4);}
  .c-error{ --c:var(--error); --cg:rgba(255,61,128,.5);} .c-none{ --c:var(--faint);}

  /* ── readout panel ──────────────────────────────────────── */
  .readout { position:sticky; top:1.2rem; border:1px solid var(--line); background:var(--panel); min-height:280px; }
  .ro-head { padding:.7rem .9rem; border-bottom:1px solid var(--line); display:flex; align-items:center; gap:.6rem;
             background:repeating-linear-gradient(0deg, rgba(0,0,0,.2) 0 2px, transparent 2px 4px); }
  .ro-head .t { font-size:.62rem; letter-spacing:.26em; color:var(--cyan); text-transform:uppercase; }
  .ro-head .v-pill { margin-left:auto; font-size:.62rem; letter-spacing:.1em; padding:.18rem .5rem; border-radius:2px; color:var(--c); border:1px solid var(--c); box-shadow:0 0 12px -4px var(--c); }
  .ro-body { padding:.9rem; }
  .ro-empty { color:var(--faint); font-size:.74rem; line-height:1.7; padding:1.4rem .3rem; text-align:center; }
  .ro-coord { font-size:.72rem; color:var(--ink2); letter-spacing:.04em; margin-bottom:.2rem; }
  .ro-coord .lib { color:var(--phos); } .ro-coord .x { color:var(--faint); margin:0 .35em; } .ro-coord .scn { color:var(--cyan); }
  .ro-sub { font-size:.62rem; color:var(--muted); letter-spacing:.08em; margin-bottom:.85rem; }
  .check { display:grid; grid-template-columns:auto 1fr; gap:.55rem; padding:.5rem 0; border-top:1px solid var(--line); }
  .check:first-child { border-top:none; }
  .check .cv { width:.55rem; align-self:start; margin-top:.35rem; height:.55rem; border-radius:50%; background:var(--c); box-shadow:0 0 8px var(--c); }
  .check .cname { font-size:.72rem; color:var(--ink2); letter-spacing:.02em; }
  .check .cverd { font-size:.56rem; letter-spacing:.12em; color:var(--c); }
  .check .cdet { grid-column:2; font-size:.7rem; color:var(--muted); line-height:1.55; margin-top:.15rem; }
  /* QoE band (delivered-% CI + A/V skew) in the readout */
  .qoe { margin-top:.7rem; padding:.5rem .6rem; border:1px solid var(--line); background:rgba(0,0,0,.18); }
  .qoe .qh { font-size:.56rem; letter-spacing:.16em; text-transform:uppercase; color:var(--faint); margin-bottom:.4rem; }
  .qbar { position:relative; height:8px; background:var(--panel2); border:1px solid var(--line); }
  .qbar .qci { position:absolute; top:0; bottom:0; background:var(--phos-dim); opacity:.7; }
  .qbar .qmean { position:absolute; top:-2px; bottom:-2px; width:2px; background:var(--ink2); box-shadow:0 0 8px var(--ink2); }
  .qsc { display:flex; justify-content:space-between; font-size:.56rem; color:var(--muted); margin-top:.25rem; font-variant-numeric:tabular-nums; }
  .qskew { font-size:.78rem; color:var(--c,var(--ink2)); font-variant-numeric:tabular-nums; }
  .qskew .qval { font-weight:700; font-size:.9rem; }
  .qoe.c-pass{ --c:var(--phos); } .qoe.c-warn{ --c:var(--amber); border-color:#5a4520; } .qoe.c-fail{ --c:var(--red); border-color:#5a2326; }
  .metrics { margin-top:.9rem; border-top:1px dashed var(--line); padding-top:.7rem; }
  .metrics .mh { font-size:.58rem; letter-spacing:.2em; color:var(--faint); text-transform:uppercase; margin-bottom:.4rem; }
  .metrics .m { display:flex; justify-content:space-between; gap:1rem; font-size:.72rem; padding:.12rem 0; }
  .metrics .m .mk { color:var(--muted); } .metrics .m .mv { color:var(--ink2); font-variant-numeric:tabular-nums; }
  .ro-err { margin-top:.8rem; color:var(--red); font-size:.7rem; border-left:2px solid var(--red); padding-left:.6rem; }

  /* ── methodology footer ─────────────────────────────────── */
  .method { margin-top:2.4rem; border-top:1px solid var(--line); padding-top:1.3rem; }
  .method h3 { font-size:.64rem; letter-spacing:.26em; color:var(--cyan); text-transform:uppercase; margin:0 0 .9rem; }
  .method .grid2 { display:grid; grid-template-columns:repeat(auto-fit,minmax(240px,1fr)); gap:1px; background:var(--line); border:1px solid var(--line); }
  .method .m { background:var(--panel); padding:.9rem 1rem; }
  .method .m b { color:var(--ink2); font-weight:700; font-size:.74rem; letter-spacing:.04em; }
  .method .m p { margin:.4rem 0 0; color:var(--muted); font-size:.72rem; line-height:1.6; }
  .method .m b .tag { color:var(--c,var(--phos)); }
  .colophon { margin-top:1.4rem; color:var(--faint); font-size:.64rem; letter-spacing:.1em; text-align:center; }
  .colophon b { color:var(--phos-dim); }
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

  <section class="compare" id="compare"></section>

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
  function buildCompare(){
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
          return {pct:deliv(res), verdict:rollup(res), scn:scn};
        });
        rows.push({si:si, proto:proto, lib:lib, tier:t1?"T1":"T2", downlink:downlink, cells:cells});
      });
    });
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
      if(res) showReadout(td.dataset.lib,td.dataset.scn,res,td);
    });
  }
  buildCompare();

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
      h+='<div class="qoe '+cls+'"><div class="qh">A/V sync skew</div><div class="qskew"><span class="qval">'+fmt(Math.round(m.avSkewMs))+' ms</span> · '+
         (sk<=400?"in sync":sk<=1500?"drifting":"DESYNCED — picture vs sound")+'</div></div>';
    }
    return h;
  }
  function showReadout(lib,scn,res,td){
    if(selected) selected.classList.remove("sel");
    selected = (td&&td.querySelector(".v")) || td; if(selected) selected.classList.add("sel");
    var rv=rollup(res), v=V[rv];
    var h='<div class="ro-head"><span class="t">Readout</span><span class="v-pill '+v.cls+'">'+v.k+'</span></div><div class="ro-body">';
    h+='<div class="ro-coord"><span class="lib">'+lib+'</span><span class="x">▸</span><span class="scn">'+scn+'</span></div>';
    h+='<div class="ro-sub">'+(res.checks?res.checks.length:0)+' invariant'+((res.checks&&res.checks.length===1)?"":"s")+' · rollup = '+v.k+'</div>';
    h+=qoeBand(res.metrics||{});
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
        td.addEventListener("click",function(){ showReadout(lib,scn,res,td); });
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
