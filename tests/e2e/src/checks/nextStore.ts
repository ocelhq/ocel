import assert from "node:assert/strict";
import { CACHE_HEADER, CACHED } from "../cacheHeaders";
import { marker } from "../html";
import { page, until } from "../nextApp";
import type { Check, CheckContext } from "./context";

const COLD_PAGES = ["1", "2", "3"];
const WARM_TIMEOUT_MS = 30_000;
const WARM_READS = 3;

interface ColdRead {
  path: string;
  tier: string | null;
  html: string;
}

async function readEachOnce(ctx: CheckContext, route: string): Promise<ColdRead[]> {
  const reads: ColdRead[] = [];
  for (const n of COLD_PAGES) {
    const path = `${route}/${n}`;
    const { res, html } = await page(ctx, path);
    reads.push({ path, tier: res.headers.get(CACHE_HEADER), html });
  }
  return reads;
}

function storeServed(reads: ColdRead[]): ColdRead {
  const served = reads.find((read) => read.tier === "HIT");
  assert.ok(
    served,
    `no first read of a prerender came from the durable store: ${JSON.stringify(reads.map(({ path, tier }) => ({ path, tier })))}`,
  );
  return served;
}

async function assertColdPageFromTheStore(ctx: CheckContext): Promise<void> {
  const served = storeServed(await readEachOnce(ctx, "/cold"));
  assert.equal(marker(served.html, "cold:static"), "prerendered at build");
}

async function assertColdShellFromTheStore(ctx: CheckContext): Promise<void> {
  const served = storeServed(await readEachOnce(ctx, "/cold-ppr"));
  assert.equal(marker(served.html, "coldppr:static"), "prerendered at build");
  assert.match(
    marker(served.html, "coldppr:live"),
    /^\d{4}-/,
    `${served.path} came without the dynamic hole the function resumes`,
  );
}

async function assertWarmShellAtTheEdge(ctx: CheckContext): Promise<void> {
  await until(WARM_TIMEOUT_MS, "/warm never answered from the edge cache", async () => {
    const { res } = await page(ctx, "/warm");
    return CACHED.includes(res.headers.get(CACHE_HEADER) as never) ? true : undefined;
  });
  const reads = [];
  for (let read = 0; read < WARM_READS; read += 1) {
    const { res, html } = await page(ctx, "/warm");
    reads.push({
      tier: res.headers.get(CACHE_HEADER),
      shell: marker(html, "warm:cached"),
      hole: marker(html, "warm:live"),
    });
  }
  assert.ok(
    reads.every((one) => CACHED.includes(one.tier as never)),
    `a warm read of /warm went past the edge cache for its shell: ${JSON.stringify(reads)}`,
  );
  assert.equal(
    new Set(reads.map((one) => one.shell)).size,
    1,
    `the shell of /warm moved between warm reads: ${JSON.stringify(reads)}`,
  );
  assert.equal(
    new Set(reads.map((one) => one.hole)).size,
    WARM_READS,
    `the function did not resume /warm's dynamic hole on every read: ${JSON.stringify(reads)}`,
  );
}

export const nextStoreReadChecks: Check[] = [
  {
    title: "a prerender nothing has read is answered from the durable store, not the function",
    run: assertColdPageFromTheStore,
  },
  {
    title:
      "a partially prerendered page nothing has read takes its shell from the durable store and only its dynamic hole from the function",
    run: assertColdShellFromTheStore,
  },
  {
    title:
      "a warm read of a partially prerendered page takes its shell from the edge cache and only its dynamic hole from the function",
    run: assertWarmShellAtTheEdge,
  },
];
