#!/usr/bin/env node

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
const STALE_WAIT_MS = (GOLDEN_REVALIDATE_SECONDS + 2) * 1_000;

const BYPASS_COOKIE = "__prerender_bypass=ocel-golden";

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
  await sleep(STALE_WAIT_MS);
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
