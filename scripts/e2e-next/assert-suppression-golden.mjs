#!/usr/bin/env node
// The golden side-effect gate for self-revalidation suppression (bd
// ocelhq-wvag.26, design §6). Asserts that `purpose: prefetch` on the edge's
// forward to the Lambda changes *whether a revalidation is started* and nothing
// else — same status, byte-identical body, identical headers modulo the ones
// that differ between any two responses.
//
// Why it exists: the suppression rests on one line of next@16.2.10
// (`if (!entry.isStale || context.isPrefetch) return entry;`,
// response-cache/index.js:201). OpenNext ships the same workaround at scale and
// carries the caveat that a change to Next's prefetch handling would break it.
// That caveat is ours now, and this — plus SUPPRESS_SELF_REVALIDATION in
// workers/nextjs/src/cache.ts, the one-line revert for both halves — is the
// tripwire.
//
// HOW THE TWO LEGS ARE MADE COMPARABLE. The edge stamps the header itself, so
// there is no request that reaches the Lambda without it on the cached path.
// Both legs are therefore sent down the BYPASS path (a draft-mode cookie), which
// forwards the client's raw headers verbatim: same route, same Lambda, same
// edge handling, and the only difference on the origin leg is the one header.
// A wrong draft cookie value is not draft mode to Next — it is ignored — so
// both legs are ordinary renders of the probe page.
//
// WHY BOTH LEGS MUST BE STALE. `purpose` is read at one place
// (`if (!entry.isStale || context.isPrefetch) return entry;`) whose first
// operand short-circuits on a fresh entry, so a probe of a freshly warmed page
// never reaches the operand under test. The probe page's window is
// GOLDEN_REVALIDATE_SECONDS and each pair is preceded by a wait past it.
//
// TWO LIMITS OF THIS GATE, recorded rather than fixed. (1) Next emits
// x-nextjs-cache only when `isSSG && !isDynamicRSCRequest && (!didPostpone ||
// isPrefetchRSCRequest)` (app-page.js), so the staleness check below cannot
// fire for a PPR route that postpones — harmless here, since those responses
// are `private, no-store` and non-cacheable, and the probe page never
// postpones. (2) the rsc variant sends `RSC: 1` without `Next-Router-Prefetch`,
// so it compares full flight payloads rather than a router prefetch's.
//
// SCOPE: this script is the harness. The AUTHORITATIVE run is the live one in
// bd ocelhq-wvag.27's session (design §9, e2e item 2). Landing it here proves
// its logic (unit tested via lib.test.mjs), not the Lambda's behaviour on a
// real substrate — nothing on this branch may claim the gate is met.
//
// Usage: assert-suppression-golden.mjs [deployment-url]
//   falls back to $NEXT_TEST_DEPLOY_URL, then $SMOKE_URL.
//
// Exits non-zero with the differences it found.

import {
  GOLDEN_MARKER,
  GOLDEN_REVALIDATE_SECONDS,
  GOLDEN_ROUTE,
  PREFETCH_PURPOSE_HEADER,
  PREFETCH_PURPOSE_VALUE,
  goldenDifferences,
} from "./lib.mjs";

const SETTLE_MS = 20_000;
const POLL_INTERVAL_MS = 3_000;
// Past the probe page's window, with room for the round-trip that warmed it.
const STALE_WAIT_MS = (GOLDEN_REVALIDATE_SECONDS + 2) * 1_000;

// Any value: hasDraftCookie (workers/nextjs/src/cache.ts) gates on the cookie's
// presence, and Next ignores a value that is not its previewModeId.
const BYPASS_COOKIE = "__prerender_bypass=ocel-golden";

// The two request shapes worth comparing: the html document, and the flight
// payload — a header that a future Next read as a router prefetch would show up
// in the RSC response first.
const VARIANTS = [
  { name: "html", headers: { accept: "text/html" } },
  { name: "rsc", headers: { accept: "text/x-component", RSC: "1" } },
];

const base = process.argv[2] || process.env.NEXT_TEST_DEPLOY_URL || process.env.SMOKE_URL;
if (!base) {
  fail("no deployment url given (argument, $NEXT_TEST_DEPLOY_URL or $SMOKE_URL)");
}
const target = new URL(GOLDEN_ROUTE, base).toString();

await settle();

let failures = 0;
for (const variant of VARIANTS) {
  // Each pair gets its own wait: the unsuppressed leg of the pair before it
  // started a render, which will have put the entry back to fresh.
  await sleep(STALE_WAIT_MS);
  // The suppressed leg first, so a difference cannot be blamed on ordering
  // against a cold Lambda: it is the one paying any cold start, and so the
  // entry is still stale when the second leg — the one that will revalidate
  // it — is sent.
  const withHeader = await probe(variant, {
    [PREFETCH_PURPOSE_HEADER]: PREFETCH_PURPOSE_VALUE,
  });
  const without = await probe(variant, {});

  const freshness = [withHeader.freshness, without.freshness];
  if (!freshness.includes("STALE")) {
    log(
      `${variant.name}: neither leg was answered from a stale entry ` +
        `(x-nextjs-cache ${freshness.join(" / ")}) — the only branch purpose: prefetch can ` +
        `change was never evaluated, so no golden claim is made for this variant`,
    );
    failures++;
    continue;
  }

  if (withHeader.tier !== without.tier) {
    log(
      `${variant.name}: legs were served by different tiers (${withHeader.tier} vs ${without.tier}) — ` +
        `not comparable, so no golden claim is made for this variant`,
    );
    failures++;
    continue;
  }

  const differences = goldenDifferences(withHeader, without);
  if (differences.length === 0) {
    log(
      `${variant.name}: identical (tier ${withHeader.tier}, x-nextjs-cache ` +
        `${freshness.join(" / ")}, ${withHeader.body.length} bytes)`,
    );
    continue;
  }
  failures++;
  log(`${variant.name}: purpose: prefetch changed the response —`);
  for (const difference of differences) log(`  ${difference}`);
}

if (failures > 0) {
  fail(
    `${failures} of ${VARIANTS.length} variants differ. purpose: prefetch is meant to change only ` +
      `whether Next starts a revalidating render. If Next's prefetch handling has changed, the revert ` +
      `is SUPPRESS_SELF_REVALIDATION = false in workers/nextjs/src/cache.ts`,
  );
}
log(`${target}: no side effect from purpose: prefetch in ${VARIANTS.length} variants`);

// --- probing ---------------------------------------------------------------

async function probe(variant, extra) {
  const response = await fetch(target, {
    headers: { ...variant.headers, cookie: BYPASS_COOKIE, ...extra },
    redirect: "manual",
  });
  return {
    status: response.status,
    headers: response.headers,
    tier: response.headers.get("x-ocel-cache") ?? "none",
    freshness: response.headers.get("x-nextjs-cache") ?? "none",
    body: await response.text(),
  };
}

// The probe page has to be serving before any difference between two fetches of
// it means anything.
async function settle() {
  const deadline = Date.now() + SETTLE_MS + POLL_INTERVAL_MS;
  let last;
  while (Date.now() < deadline) {
    last = await probe(VARIANTS[0], {}).catch((error) => ({ status: 0, error: error.message, body: "" }));
    if (last.status === 200 && last.body.includes(GOLDEN_MARKER)) return;
    await sleep(POLL_INTERVAL_MS);
  }
  fail(
    `${target} never served the golden probe page (last: status ${last?.status}` +
      `${last?.error ? `, ${last.error}` : ""}) — no golden claim can be made`,
  );
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function log(message) {
  process.stdout.write(`[ocel-e2e] golden: ${message}\n`);
}

function fail(message) {
  process.stderr.write(`[ocel-e2e] golden assertion failed: ${message}\n`);
  process.exit(1);
}
