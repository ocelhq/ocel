// Pure helpers for the Next.js adapter-harness lifecycle scripts (deploy.mjs,
// logs.mjs, cleanup.mjs, merge-baseline.mjs). The harness runs them as separate
// processes, so everything they share travels through files in the temp app
// directory rather than memory.
//
// Everything framework-agnostic — project/slug derivation, the ocel.config.ts
// renderer, the bytecode-cache key shape and its CloudWatch line matching, the
// tar/zip readers — lives in @ocel-scripts/e2e-shared and is re-exported below
// so every existing import from "./lib.mjs" keeps working unchanged. Only what
// is genuinely Next-specific (ISR, the tag publisher, the golden-header
// comparison, the harness's own marker lines, the baseline manifest) is
// defined here.

import { projectSlug, renderOcelConfig as renderSharedOcelConfig } from "@ocel-scripts/e2e-shared/lib.mjs";

// STATE_FILE, BUILD_LOG_FILE and DEPLOY_RESULT_FILE come from this re-export
// rather than being redeclared here — see their doc comments in
// @ocel-scripts/e2e-shared/lib.mjs.
export * from "@ocel-scripts/e2e-shared/lib.mjs";

/**
 * The name every temp app is declared under. Isolation lives in the project slug
 * (projectSlug), so a per-app name would buy nothing and cost length: the
 * Cloudflare worker script name derived from both ("ocel-<slug>-preview-<app>")
 * has 63 characters to fit in.
 */
export const APP_NAME = "app";

/**
 * projectSlugForApp is how deploy.mjs and cleanup.mjs derive the slug. Both must
 * derive the same one from the same environment — a drift between them means
 * cleanup tears down the wrong project, or nothing — so the environment read
 * lives here, in one place, rather than at each call site.
 */
export function projectSlugForApp(appDir) {
  return projectSlug({ runId: process.env.GITHUB_RUN_ID, dir: process.env.NEXT_TEST_DIR || appDir });
}

/**
 * renderOcelConfig is the ocel.config.ts written into the temp app: the shared
 * renderer, fixed to Next's own single app declaration (APP_NAME, framework
 * "next") so every call site here keeps the exact shape it always has.
 */
export function renderOcelConfig({ slug, previewDomain }) {
  return renderSharedOcelConfig({ slug, previewDomain, apps: [{ name: APP_NAME, path: ".", framework: "next" }] });
}

/**
 * withBuildScript returns the app's package.json with a `build` script, which
 * buildNext requires (it throws without one). An app that already declares one
 * keeps it: the fixture may build with flags of its own.
 */
export function withBuildScript(pkg) {
  if (pkg.scripts?.build) {
    return pkg;
  }
  return { ...pkg, scripts: { ...pkg.scripts, build: "next build" } };
}

/**
 * The smoke app's revalidation probe, and the window it declares. Mirrors
 * `revalidate` and the marker in smoke-app/app/isr/page.tsx — assert-isr.mjs
 * reads them from here so the page and its assertion cannot drift apart.
 */
export const ISR_ROUTE = "/isr";
export const ISR_REVALIDATE_SECONDS = 5;

/**
 * isrToken pulls the probe page's per-render token out of its html. Null when
 * the marker is absent, which means the response was not that page at all —
 * a redirect, an error page, or a build that dropped the route — and must be
 * reported as such rather than compared as a value.
 */
export function isrToken(html) {
  return /isr-token:(\d+)/.exec(String(html ?? ""))?.[1] ?? null;
}

/**
 * The tag-publisher probe's route, and the tag it raises. Mirrors
 * smoke-app/app/api/revalidate-tag/route.ts — assert-tag-publisher.mjs reads
 * them from here so the route and its assertion cannot drift apart.
 */
export const TAG_PROBE_ROUTE = "/api/revalidate-tag";

/**
 * tagProbeTag names one run's invalidation. It must be unique per run: the
 * assertion proves the publisher carried *this* raise, and a tag some earlier
 * run already published would be found in the snapshot before the probe fired.
 */
export function tagProbeTag(stamp) {
  return `ocel-publisher-probe-${stamp}`;
}

/**
 * The golden comparison's probe route and the marker its body carries. Mirrors
 * smoke-app/app/golden/page.tsx — assert-suppression-golden.mjs reads them from
 * here so the page and its assertion cannot drift apart.
 */
export const GOLDEN_ROUTE = "/golden";
export const GOLDEN_MARKER = "golden-body:v1";

/**
 * The probe page's own `revalidate`, kept short on purpose. `purpose: prefetch`
 * is read at exactly one place in Next's response cache
 * (`if (!entry.isStale || context.isPrefetch) return entry;`), where the FIRST
 * operand short-circuits on a fresh entry — so a comparison made against a
 * freshly warmed page proves only that the header does not change a fresh
 * serve, and never evaluates the branch it is guarding. The assertion waits out
 * this window before each pair so both legs are answered from a STALE entry,
 * which is the only state where `purpose` can change anything — and the state
 * suppression puts every governed route into.
 */
export const GOLDEN_REVALIDATE_SECONDS = 3;

/**
 * The header the edge stamps on a user-path forward to suppress Next's own
 * self-revalidation (bd ocelhq-wvag.26, workers/nextjs/src/index.ts).
 */
export const PREFETCH_PURPOSE_HEADER = "purpose";
export const PREFETCH_PURPOSE_VALUE = "prefetch";

/**
 * Headers a golden comparison must ignore, because they differ between any two
 * responses whatever the request carried:
 *
 * - `date`/`age`: the responses are seconds apart.
 * - `x-nextjs-cache`: the freshness of the entry each render was answered from,
 *   which is what the suppression legitimately changes.
 * - `x-ocel-cache`: the tier that answered. Compared separately by the
 *   assertion, which requires both legs to report the same one — a difference
 *   there means the legs were never comparable, not that the render differed.
 * - the Cloudflare and connection-level set: stamped per response by the edge
 *   and the transport, never by the render.
 * - the `x-amzn-*` set: the Lambda Function URL stamps a fresh request id, trace
 *   id and receive date on every invocation and the edge forwards them, so they
 *   differ between any two responses by construction. They reached this
 *   comparison for the first time on the live run for ocelhq-wvag.27 — a local
 *   run has no Function URL in front of it — and failed both variants on nothing
 *   but per-invocation ids while the bodies were byte-identical.
 *
 * Everything else — status, body bytes, content-type, etag, x-matched-path,
 * x-nextjs-postponed, Next's own vary — is compared, which is the point: the
 * caveat this gate exists for is that a future Next could make `purpose`
 * change what is rendered.
 */
export const GOLDEN_VOLATILE_HEADERS = new Set([
  "date",
  "age",
  "x-nextjs-cache",
  "x-ocel-cache",
  "cf-ray",
  "cf-cache-status",
  "x-amzn-requestid",
  "x-amzn-trace-id",
  "x-amzn-remapped-date",
  "server-timing",
  "report-to",
  "nel",
  "alt-svc",
  "connection",
  "keep-alive",
  "transfer-encoding",
  "content-encoding",
  "content-length",
]);

function headerMap(headers) {
  const entries =
    typeof headers?.entries === "function" ? [...headers.entries()] : Object.entries(headers ?? {});
  const map = new Map();
  for (const [name, value] of entries) {
    const lower = String(name).toLowerCase();
    if (GOLDEN_VOLATILE_HEADERS.has(lower)) continue;
    map.set(lower, String(value));
  }
  return map;
}

/**
 * goldenDifferences compares two fetches of the same route that differ only in
 * whether the origin leg carried `purpose: prefetch`, and returns one line per
 * difference — empty when the header had no observable side effect.
 *
 * A leg is `{ status, headers, body }`. Bodies are compared as exact strings:
 * the probe page renders no clock and no request data, so any difference at all
 * is the header's.
 */
export function goldenDifferences(withHeader, without) {
  const differences = [];
  if (withHeader?.status !== without?.status) {
    differences.push(`status: with=${withHeader?.status} without=${without?.status}`);
  }
  if (withHeader?.body !== without?.body) {
    differences.push(
      `body: ${byteDiff(String(withHeader?.body ?? ""), String(without?.body ?? ""))}`,
    );
  }
  const a = headerMap(withHeader?.headers);
  const b = headerMap(without?.headers);
  for (const name of [...new Set([...a.keys(), ...b.keys()])].sort()) {
    if (a.get(name) !== b.get(name)) {
      differences.push(`header ${name}: with=${a.get(name) ?? "(absent)"} without=${b.get(name) ?? "(absent)"}`);
    }
  }
  return differences;
}

/** Where two bodies first diverge, and by how much — enough to act on. */
function byteDiff(a, b) {
  if (a.length !== b.length) return `lengths ${a.length} vs ${b.length}`;
  const at = [...a].findIndex((char, index) => char !== b[index]);
  return `differs at offset ${at}: ${JSON.stringify(a.slice(at, at + 40))} vs ${JSON.stringify(b.slice(at, at + 40))}`;
}

/**
 * markerLines formats the three lines the harness parses out of the logs
 * script's output. A missing value is reported as the literal "undefined",
 * which is what the contract asks for — never a blank value.
 *
 * `DEPLOYMENT_ID` is the harness's own marker name, not ours: it is an external
 * contract, so it keeps that spelling while carrying Ocel's promotion id.
 * IMMUTABLE_ASSET_TOKEN is always undefined — the Ocel adapter never sets
 * `config.deploymentId`, so its assets carry no `?dpl=` token.
 */
export function markerLines({ buildId, promotionId }) {
  return [
    `BUILD_ID: ${buildId || "undefined"}`,
    `DEPLOYMENT_ID: ${promotionId || "undefined"}`,
    `IMMUTABLE_ASSET_TOKEN: undefined`,
  ];
}

/** Recovers the harness's test-file key from `${test.file}.results.json`. */
export function suiteFromResultsPath(resultsPath) {
  return resultsPath
    .replace(/\\/g, "/")
    .replace(/^\.\//, "")
    .replace(/\.results\.json$/, "");
}

/**
 * suiteResultFromJest reduces one suite's Jest JSON output to the baseline
 * manifest's per-suite entry. Case names are the full "ancestors > title" path,
 * which is what the harness's excludedCases matches on.
 *
 * A suite that produced no assertions at all did not merely fail — it never
 * ran (a crash while loading, a deploy the harness could not reach), which the
 * manifest records as runtimeError so the whole file is skipped rather than
 * every case in it being listed.
 */
export function suiteResultFromJest(results) {
  const passed = [];
  const failed = [];
  for (const suite of results?.testResults ?? []) {
    for (const assertion of suite?.assertionResults ?? []) {
      const name = [...(assertion.ancestorTitles ?? []), assertion.title].filter(Boolean).join(" > ");
      if (assertion.status === "passed") {
        passed.push(name);
      } else if (assertion.status === "failed") {
        failed.push(name);
      }
    }
  }
  return { passed, failed, flakey: [], runtimeError: passed.length === 0 && failed.length === 0 };
}

/**
 * buildBaselineManifest turns one group's collected Jest results files into a
 * baseline manifest fragment, keyed by test file — the legacy (unversioned)
 * NEXT_EXTERNAL_TESTS_FILTERS shape, where a listed suite's `failed` cases are
 * excluded and any newly added case is automatically included.
 */
export function buildBaselineManifest(files) {
  const manifest = {};
  for (const { path, results } of files) {
    manifest[suiteFromResultsPath(path)] = suiteResultFromJest(results);
  }
  return manifest;
}

/**
 * mergeBaselineManifest folds every group's fragment into the one manifest the
 * repo commits. Suites are disjoint across groups in a single run, but merging
 * is defined anyway — a re-run of one group must not drop the others' entries —
 * and a suite any group saw crash stays marked as a runtime error. Keys are
 * sorted so the committed baseline diffs cleanly.
 */
export function mergeBaselineManifest(manifests) {
  const merged = {};
  for (const manifest of manifests) {
    for (const [suite, entry] of Object.entries(manifest ?? {})) {
      const prior = merged[suite];
      if (!prior) {
        merged[suite] = {
          passed: [...(entry.passed ?? [])],
          failed: [...(entry.failed ?? [])],
          flakey: [...(entry.flakey ?? [])],
          runtimeError: Boolean(entry.runtimeError),
        };
        continue;
      }
      merged[suite] = {
        passed: union(prior.passed, entry.passed),
        failed: union(prior.failed, entry.failed),
        flakey: union(prior.flakey, entry.flakey),
        runtimeError: prior.runtimeError || Boolean(entry.runtimeError),
      };
    }
  }
  return Object.fromEntries(Object.keys(merged).sort().map((suite) => [suite, merged[suite]]));
}

function union(a, b) {
  return [...new Set([...(a ?? []), ...(b ?? [])])];
}
