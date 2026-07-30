#!/usr/bin/env node
// Asserts that a revalidating route's cache entry is actually *rewritten* on a
// live deployment — not merely that the route answers 200.
//
// Why this exists as its own assertion: the worker decides whether a prerender
// tier may refresh itself with `const revalidates = !edgeEntryKey`
// (workers/nextjs/src/index.ts). If that field is ever wrong — an entry key
// landing in the edge slot, say — every route still serves correctly and only
// revalidation is dead. A 200 check cannot see it; a changing body on a cached
// tier can.
//
// Usage: assert-isr.mjs [deployment-url]
//   falls back to $NEXT_TEST_DEPLOY_URL, then $SMOKE_URL.
//
// Exits non-zero with the observations it collected.

import { ISR_REVALIDATE_SECONDS, ISR_ROUTE, isrToken } from "./lib.mjs";

// The colo cache and the R2 store both sit in front of the Lambda, so a rewrite
// takes a few requests to become visible: one to find the entry stale and
// schedule the refresh, later ones to read what it wrote.
const SETTLE_MS = 20_000;
const POLL_INTERVAL_MS = 3_000;
const CHANGE_DEADLINE_MS = 150_000;

// The tiers that mean "this body came out of a cache entry". A token that only
// ever changes on a MISS proves a fresh render, not a rewritten entry.
const CACHED_TIERS = new Set(["HIT", "PRERENDER", "STALE"]);

const base = process.argv[2] || process.env.NEXT_TEST_DEPLOY_URL || process.env.SMOKE_URL;
if (!base) {
  fail("no deployment url given (argument, $NEXT_TEST_DEPLOY_URL or $SMOKE_URL)");
}
const target = new URL(ISR_ROUTE, base).toString();

const first = await settle();
log(`initial token ${first.token} (${first.tier})`);
if (!CACHED_TIERS.has(first.tier)) {
  fail(
    `${target} served from tier "${first.tier}", so it is not a cached prerender at all — ` +
      `the route lost its revalidate config, or the manifest no longer resolves it to a prerender`,
  );
}

log(`waiting out the ${ISR_REVALIDATE_SECONDS}s revalidate window, then polling for a rewrite`);
await sleep(ISR_REVALIDATE_SECONDS * 1000 + SETTLE_MS);

const deadline = Date.now() + CHANGE_DEADLINE_MS;
const tiersSeen = new Set();
let changedOnMiss = null;
while (Date.now() < deadline) {
  const seen = await probe();
  tiersSeen.add(seen.tier);
  if (seen.token && seen.token !== first.token) {
    if (CACHED_TIERS.has(seen.tier)) {
      log(`entry rewritten: ${first.token} -> ${seen.token} (${seen.tier})`);
      process.exit(0);
    }
    // A fresh render can change the body without anything being stored, so this
    // is noted and polling continues rather than counted as the proof.
    changedOnMiss = seen;
  }
  await sleep(POLL_INTERVAL_MS);
}

if (changedOnMiss) {
  fail(
    `${target} only changed on an uncached tier ("${changedOnMiss.tier}"): the route re-renders but no ` +
      `cache entry is being written, so nothing is revalidating`,
  );
}
fail(
  `${target} still serves token ${first.token} after ${Math.round(CHANGE_DEADLINE_MS / 1000)}s past a ` +
    `${ISR_REVALIDATE_SECONDS}s revalidate window (tiers seen: ${[...tiersSeen].join(", ") || "none"}). ` +
    `Revalidation is dead — suspect the worker's \`revalidates = !edgeEntryKey\` and the adapter's ` +
    `entryKey/edgeEntryKey split for node-parented prerenders`,
);

// --- probing ---------------------------------------------------------------

async function probe() {
  try {
    const response = await fetch(target, { headers: { accept: "text/html" }, redirect: "manual" });
    const body = await response.text();
    return {
      status: response.status,
      tier: response.headers.get("x-ocel-cache") ?? "none",
      token: response.status === 200 ? isrToken(body) : null,
    };
  } catch (error) {
    return { status: 0, tier: "none", token: null, error: error.message };
  }
}

// The first readable token, once the deployment is actually serving the route.
async function settle() {
  const deadline = Date.now() + SETTLE_MS + POLL_INTERVAL_MS;
  let last;
  while (Date.now() < deadline) {
    last = await probe();
    if (last.token) return last;
    await sleep(POLL_INTERVAL_MS);
  }
  fail(
    `${target} never served the probe page (last: status ${last?.status}, tier ${last?.tier}` +
      `${last?.error ? `, ${last.error}` : ""}) — no revalidation claim can be made`,
  );
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function log(message) {
  process.stdout.write(`[ocel-e2e] isr: ${message}\n`);
}

function fail(message) {
  process.stderr.write(`[ocel-e2e] isr assertion failed: ${message}\n`);
  process.exit(1);
}
