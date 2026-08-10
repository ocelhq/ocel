#!/usr/bin/env node

import { readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const HERE = dirname(fileURLToPath(import.meta.url));
const state = JSON.parse(readFileSync(join(HERE, "sweep-state.json"), "utf8"));

const esc = (s) =>
  String(s ?? "").replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" })[c]);

const BEADS_UI_URL = process.env.BEADS_UI_URL ?? "http://localhost:7070";
const isBeadsId = (id) => /^ocelhq-[a-z0-9]+$/.test(id);
const issueRef = (id) =>
  BEADS_UI_URL && isBeadsId(id)
    ? `<a class="iss" href="${esc(BEADS_UI_URL)}/#/issues?issue=${encodeURIComponent(id)}"><code>${esc(id)}</code></a>`
    : `<code>${esc(id)}</code>`;

const suites = Object.entries(state.suites);
const covered = suites.length;
const total = state.totalSuitesInTree;
const pct = (covered / total) * 100;

const agg = suites.reduce(
  (a, [, s]) => {
    a.passed += s.counts.passed;
    a.failed += s.counts.failed;
    a.known += s.counts.known;
    a.skipped += s.counts.skipped ?? 0;
    return a;
  },
  { passed: 0, failed: 0, known: 0, skipped: 0 },
);

const isAdapter = (i) => i.kind === "adapter";
const issues = Object.entries(state.issues)
  .filter(([, i]) => i.tests > 0)
  .sort((a, b) => b[1].tests - a[1].tests);
const maxTests = Math.max(...issues.map(([, i]) => i.tests));
const adapterTests = issues.filter(([, i]) => isAdapter(i)).reduce((n, [, i]) => n + i.tests, 0);
const otherTests = issues.filter(([, i]) => !isAdapter(i)).reduce((n, [, i]) => n + i.tests, 0);

const rate = (agg.passed / (agg.passed + agg.failed)) * 100;
const adjRate = (agg.passed / (agg.passed + adapterTests)) * 100;

const reach = {};
for (const [path, s] of suites) {
  for (const id of Object.values(s.failedTests ?? {})) {
    if (!id) continue;
    (reach[id] ??= new Set()).add(path.split("/").slice(3).join("/").replace(/\.test\.ts$/, ""));
  }
}

const bar = (id, i) => {
  const w = (i.tests / maxTests) * 100;
  const cls = isAdapter(i) ? "adapter" : "other";
  const seen = [...(reach[id] ?? [])];
  return `<div class="row" title="${esc(i.oneLine)}">
    <div class="rl">${issueRef(id)}${i.priority ? `<span class="pri ${esc(i.priority)}">${esc(i.priority)}</span>` : ""}</div>
    <div class="rt">
      <div class="track"><div class="fill ${cls}" style="inline-size:${w.toFixed(1)}%"></div></div>
      <span class="val">${i.tests}</span>
    </div>
    <div class="rd">${esc(i.oneLine)}${seen.length ? `<span class="reach">seen in ${seen.length} suite${seen.length === 1 ? "" : "s"}: ${esc(seen.join(", "))}</span>` : ""}</div>
  </div>`;
};

const runStats = (r) => {
  const c = r.counts ?? r.totals ?? r;
  const n = (v) => (typeof v === "number" ? v : null);
  return {
    suites: n(r.suites) ?? n(r.suitesRun) ?? n(r.totals?.suites),
    passed: n(c.passed),
    failed: n(c.failed),
  };
};
const cell = (v) => (v === null ? "—" : v);
const rateCell = (p, f) =>
  p === null || f === null || p + f === 0 ? "—" : `${((p / (p + f)) * 100).toFixed(1)}%`;

const runsTrend =
  state.runs.length < 2
    ? `<p class="muted">A trend needs at least two runs; this is run ${state.runs.length}. The table below is the record so far.</p>`
    : `<div class="wrap"><table><tr><th>Sweep</th><th class="n">Suites</th><th class="n">Passed</th><th class="n">Failed</th><th class="n">Pass rate</th></tr>
      ${state.runs
        .map((r) => {
          const { suites, passed, failed } = runStats(r);
          return `<tr><td><code>${esc(r.sweepId)}</code></td><td class="n">${cell(suites)}</td><td class="n">${cell(passed)}</td><td class="n">${cell(failed)}</td><td class="n">${rateCell(passed, failed)}</td></tr>`;
        })
        .join("")}</table></div>`;

const html = `<title>Ocel Next.js adapter — cumulative e2e coverage</title>
<style>
  .viz-root, body {
    color-scheme: light;
    --surface-1:#fcfcfb; --surface-2:#f4f3ef;
    --text-primary:#0b0b0b; --text-secondary:#52514e; --text-muted:#898781;
    --grid:#e1e0d9; --baseline:#c3c2b7;
    --series-adapter:#2a78d6; --series-other:#eb6834;
    --good:#0ca30c; --critical:#d03b3b; --warning:#fab219;
    --link:#1f5bb5;
  }
  @media (prefers-color-scheme: dark) {
    :root:where(:not([data-theme="light"])) .viz-root,
    :root:where(:not([data-theme="light"])) body {
      color-scheme: dark;
      --surface-1:#1a1a19; --surface-2:#242422;
      --text-primary:#fff; --text-secondary:#c3c2b7; --text-muted:#898781;
      --grid:#2c2c2a; --baseline:#383835;
      --series-adapter:#3987e5; --series-other:#d95926;
      --link:#8ab4ff;
    }
  }
  :root[data-theme="dark"] .viz-root, :root[data-theme="dark"] body {
    color-scheme: dark;
    --surface-1:#1a1a19; --surface-2:#242422;
    --text-primary:#fff; --text-secondary:#c3c2b7; --text-muted:#898781;
    --grid:#2c2c2a; --baseline:#383835;
    --series-adapter:#3987e5; --series-other:#d95926;
    --link:#8ab4ff;
  }
  * { box-sizing:border-box; }
  body { margin:0 auto; padding:2rem 1.25rem 4rem; max-width:68rem; background:var(--surface-1);
         color:var(--text-primary); font:15px/1.6 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif; }
  h1 { font-size:1.55rem; margin:0 0 .2rem; letter-spacing:-.015em; }
  h2 { font-size:1.05rem; margin:2.75rem 0 .5rem; }
  .sub { color:var(--text-secondary); margin:0 0 2rem; }
  .muted { color:var(--text-muted); }
  code { font-family:ui-monospace,SFMono-Regular,Menlo,monospace; font-size:.86em; }

  /* Issue ids deep-link into the beads kanban UI. Underline on hover only —
     these appear in dense tables where a permanent rule turns into noise. */
  a.iss { color:inherit; text-decoration:none; border-block-end:1px dotted var(--text-muted); }
  a.iss:hover, a.iss:focus-visible { color:var(--link); border-block-end-style:solid; border-block-end-color:currentColor; }
  a.iss:focus-visible { outline:2px solid var(--link); outline-offset:2px; border-radius:2px; }

  .hero { border:1px solid var(--grid); border-radius:.6rem; padding:1.25rem 1.4rem; background:var(--surface-2); }
  .hero .big { font-size:2.9rem; font-weight:600; letter-spacing:-.03em; line-height:1; font-variant-numeric:tabular-nums; }
  .hero .cap { color:var(--text-secondary); margin-top:.3rem; }
  .track { background:var(--grid); border-radius:4px; block-size:10px; overflow:hidden; flex:1; }
  .fill { block-size:100%; border-radius:0 4px 4px 0; }
  .fill.adapter { background:var(--series-adapter); }
  .fill.other   { background:var(--series-other); }
  .fill.cov     { background:var(--series-adapter); }
  .hero .track { block-size:12px; margin-top:1rem; }

  .tiles { display:grid; grid-template-columns:repeat(auto-fit,minmax(8rem,1fr)); gap:.7rem; margin:1.25rem 0; }
  .tile { border:1px solid var(--grid); border-radius:.5rem; padding:.8rem .95rem; background:var(--surface-2); }
  .tile .n { font-size:1.65rem; font-weight:600; line-height:1.1; letter-spacing:-.02em; font-variant-numeric:tabular-nums; }
  .tile .l { color:var(--text-muted); font-size:.75rem; text-transform:uppercase; letter-spacing:.05em; }
  .tile .n.good{color:var(--good)} .tile .n.bad{color:var(--critical)} .tile .n.warn{color:var(--warning)}

  .legend { display:flex; gap:1.1rem; flex-wrap:wrap; margin:.5rem 0 1rem; font-size:.85rem; color:var(--text-secondary); }
  .legend i { display:inline-block; inline-size:11px; block-size:11px; border-radius:3px; margin-inline-end:.4rem; vertical-align:-1px; }

  .row { display:grid; grid-template-columns:13rem 1fr; gap:.35rem .9rem; align-items:center;
         padding:.5rem 0; border-block-end:1px solid var(--grid); }
  .row:last-child { border-block-end:0; }
  .rl { display:flex; align-items:center; gap:.4rem; flex-wrap:wrap; }
  .rt { display:flex; align-items:center; gap:.6rem; }
  .val { font-variant-numeric:tabular-nums; font-weight:600; min-inline-size:1.6rem; }
  .rd { grid-column:2; color:var(--text-secondary); font-size:.84rem; margin-top:-.2rem; }
  .reach { display:block; color:var(--text-muted); font-size:.79rem; }
  .pri { font-size:.68rem; padding:.05rem .35rem; border-radius:3px; border:1px solid var(--baseline); color:var(--text-muted); }
  .pri.P0 { color:var(--critical); border-color:var(--critical); }

  .wrap { overflow-x:auto; }
  table { border-collapse:collapse; inline-size:100%; font-size:.86rem; }
  th,td { text-align:left; padding:.4rem .65rem; border-block-end:1px solid var(--grid); }
  th { color:var(--text-muted); font-size:.74rem; text-transform:uppercase; letter-spacing:.05em; font-weight:600; }
  td.n, th.n { text-align:right; font-variant-numeric:tabular-nums; }
  details { border:1px solid var(--grid); border-radius:.45rem; padding:.6rem .9rem; background:var(--surface-2); margin:1rem 0; }
  summary { cursor:pointer; color:var(--text-secondary); }
  .note { border:1px solid var(--grid); border-inline-start:3px solid var(--warning);
          border-radius:.45rem; padding:.8rem 1rem; background:var(--surface-2); margin:1.25rem 0; }
</style>
<div class="viz-root">

<h1>Ocel Next.js adapter — cumulative e2e coverage</h1>
<p class="sub">Generated from <code>sweep-state.json</code> · last sweep <code>${esc(state.lastSweepId)}</code> · ${esc(state.lastRunAt)}</p>

<div class="hero">
  <div class="big">${covered} <span style="font-size:1.3rem;font-weight:400;color:var(--text-secondary)">of ${total} suites</span></div>
  <div class="cap">${pct.toFixed(1)}% of <code>test/e2e/app-dir</code> covered — <strong>${total - covered}</strong> never run</div>
  <div class="track"><div class="fill cov" style="inline-size:${Math.max(pct, 0.4).toFixed(2)}%"></div></div>
</div>

<h2>Test cases across every suite covered</h2>
<div class="tiles">
  <div class="tile"><div class="n good">${agg.passed}</div><div class="l">Passed</div></div>
  <div class="tile"><div class="n bad">${agg.failed}</div><div class="l">Failed</div></div>
  <div class="tile"><div class="n warn">${agg.known}</div><div class="l">Known</div></div>
  <div class="tile"><div class="n">${agg.skipped}</div><div class="l">Skipped</div></div>
  <div class="tile"><div class="n">${rate.toFixed(1)}%</div><div class="l">Pass rate</div></div>
  <div class="tile"><div class="n">${adjRate.toFixed(1)}%</div><div class="l">Adapter-only</div></div>
</div>
<p class="muted">Pass rate is <code>passed / (passed + failed)</code>; <em>known</em> and <em>skipped</em> are excluded from both
sides. <strong>Adapter-only</strong> additionally excludes the ${otherTests} failures traced to the environment or to the
test itself — that is the number that reflects adapter quality.</p>

<h2>Recurring failure causes</h2>
<div class="legend">
  <span><i style="background:var(--series-adapter)"></i>Adapter defect (${adapterTests} tests)</span>
  <span><i style="background:var(--series-other)"></i>Environment / not a defect (${otherTests} tests)</span>
</div>
${issues.map(([id, i]) => bar(id, i)).join("\n")}

<details>
  <summary>Same data as a table</summary>
  <div class="wrap"><table>
    <tr><th>Issue</th><th class="n">Tests</th><th>Priority</th><th>Kind</th><th>Cause</th></tr>
    ${issues
      .map(
        ([id, i]) =>
          `<tr><td>${issueRef(id)}</td><td class="n">${i.tests}</td><td>${esc(i.priority ?? "")}</td><td>${esc(i.kind)}</td><td>${esc(i.oneLine)}</td></tr>`,
      )
      .join("")}
  </table></div>
</details>

<h2>Suites covered</h2>
<div class="wrap">
<table>
  <tr><th>Suite</th><th class="n">Passed</th><th class="n">Failed</th><th class="n">Known</th><th>Last run</th><th>Open issues</th></tr>
  ${suites
    .sort((a, b) => b[1].counts.failed - a[1].counts.failed)
    .map(
      ([path, s]) =>
        `<tr><td><code>${esc(path.replace(/^test\/e2e\//, ""))}</code></td><td class="n">${s.counts.passed}</td><td class="n">${s.counts.failed}</td><td class="n">${s.counts.known}</td><td><code>${esc(s.lastRun)}</code></td><td>${(s.openIssues ?? []).map((i) => issueRef(i)).join(" ") || "—"}</td></tr>`,
    )
    .join("")}
</table>
</div>

<h2>Run history</h2>
${runsTrend}

${
  state.runs.some((r) => r.lambdaConcurrencyQuota && r.lambdaConcurrencyQuota < 100)
    ? `<div class="note"><strong>These numbers carry an environment caveat.</strong> ${state.runs
        .filter((r) => r.lambdaConcurrencyQuota < 100)
        .map((r) => `<code>${esc(r.sweepId)}</code> ran with a Lambda concurrency quota of ${r.lambdaConcurrencyQuota}`)
        .join("; ")}. Throttled requests surface as 502 and are indistinguishable from origin errors without checking for
        <code>x-amzn-requestid</code>. Re-run the affected suites once the quota is raised.</div>`
    : ""
}

<h2>Known-cause registry</h2>
<p class="muted">Every open issue carries machine-matchable signatures so a later sweep can attribute a familiar failure
without spending a debugger budget rediscovering it. <strong>Specific</strong> matchers auto-attribute;
<strong>broad</strong> ones must pass the stated check first, so a genuinely new bug is not hidden behind a familiar error.</p>
<div class="wrap">
<table>
  <tr><th>Issue</th><th>Matcher</th><th>Confidence</th><th>Check before attributing</th></tr>
  ${Object.entries(state.issues)
    .flatMap(([id, i]) =>
      (i.matchers ?? []).map(
        (m) =>
          `<tr><td>${issueRef(id)}</td><td><code>${esc(m.pattern)}</code></td><td>${esc(m.confidence)}</td><td>${esc(m.check ?? "—")}</td></tr>`,
      ),
    )
    .join("")}
</table>
</div>
</div>
`;

writeFileSync(join(HERE, "coverage.html"), html);
console.log(
  `coverage.html — ${covered}/${total} suites (${pct.toFixed(1)}%), ` +
    `${agg.passed}P/${agg.failed}F, rate ${rate.toFixed(1)}% (adapter-only ${adjRate.toFixed(1)}%), ` +
    `${issues.length} causes charted`,
);
