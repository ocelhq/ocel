import assert from "node:assert/strict";
import { marker } from "../html";
import { page, steady, until } from "../nextApp";
import type { Check, CheckContext } from "./context";

const REVALIDATION_TIMEOUT_MS = 60_000;
const STEADY_READS = 3;
const STEADY_ATTEMPTS = 3;
const ENTRY_SOURCE_HEADER = "x-ocel-entry-source";
const FALLBACK_TAG = "pprid-shell";

async function readMarker(ctx: CheckContext, path: string, name: string): Promise<string> {
  return marker((await page(ctx, path)).html, name);
}

function readSteadyShell(ctx: CheckContext, path: string, scope: string): Promise<string> {
  return steady(
    () => readMarker(ctx, path, `${scope}:cached`),
    path,
    STEADY_READS,
    STEADY_ATTEMPTS,
  );
}

async function revalidate(ctx: CheckContext, query: string): Promise<void> {
  const res = await ctx.fetch(`${ctx.baseUrl}/api/next/revalidate?${query}`, { method: "POST" });
  assert.equal(res.status, 200, `revalidating with ${query} answered ${res.status}`);
  await res.arrayBuffer();
}

async function waitForNewShell(
  ctx: CheckContext,
  path: string,
  scope: string,
  from: string,
): Promise<string> {
  return until(REVALIDATION_TIMEOUT_MS, `${path} never moved off ${from}`, async () => {
    const now = await readMarker(ctx, path, `${scope}:cached`);
    return now === from ? undefined : now;
  });
}

async function assertShellStaysMoved(
  ctx: CheckContext,
  path: string,
  scope: string,
  from: string,
  to: string,
): Promise<void> {
  for (let read = 0; read < STEADY_READS; read += 1) {
    const seen = await readMarker(ctx, path, `${scope}:cached`);
    assert.notEqual(seen, from, `${path} went back to the shell it had before it was revalidated`);
    assert.equal(seen, to, `${path} moved again with nothing revalidating it`);
  }
}

async function assertShellStableWhileHoleMoves(ctx: CheckContext): Promise<void> {
  const reads = [];
  for (let read = 0; read < STEADY_READS; read += 1) {
    const { html } = await page(ctx, "/ppr");
    reads.push({ shell: marker(html, "ppr:cached"), hole: marker(html, "ppr:live") });
  }
  assert.equal(
    new Set(reads.map((one) => one.shell)).size,
    1,
    `the static shell moved between reads: ${JSON.stringify(reads)}`,
  );
  assert.ok(
    new Set(reads.map((one) => one.hole)).size > 1,
    `the dynamic hole answered every read with the same render: ${JSON.stringify(reads)}`,
  );
}

async function assertParamsShareTheFallbackShell(ctx: CheckContext): Promise<void> {
  const alpha = (await page(ctx, "/ppr/alpha")).html;
  const beta = (await page(ctx, "/ppr/beta")).html;
  assert.equal(marker(alpha, "pprid:id"), "alpha");
  assert.equal(marker(beta, "pprid:id"), "beta");
  assert.equal(
    marker(alpha, "pprid:cached"),
    marker(beta, "pprid:cached"),
    "two params the build did not prerender were answered with different shells",
  );
}

async function assertPathRevalidationMovesTheShell(ctx: CheckContext): Promise<void> {
  const before = await readSteadyShell(ctx, "/ppr", "ppr");
  await revalidate(ctx, `path=${encodeURIComponent("/ppr")}`);
  const after = await waitForNewShell(ctx, "/ppr", "ppr", before);
  await assertShellStaysMoved(ctx, "/ppr", "ppr", before, after);
}

async function assertTagRevalidationMovesTheFallbackShell(ctx: CheckContext): Promise<void> {
  const before = await readSteadyShell(ctx, "/ppr/alpha", "pprid");
  await revalidate(ctx, `tag=${FALLBACK_TAG}`);
  const after = await waitForNewShell(ctx, "/ppr/alpha", "pprid", before);
  assert.equal(
    await readMarker(ctx, "/ppr/beta", "pprid:cached"),
    after,
    "revalidating the fallback shell's tag moved it for one param and not another",
  );
}

export const nextPprChecks: Check[] = [
  {
    title: "a partially prerendered page keeps one static shell while its dynamic hole moves",
    run: assertShellStableWhileHoleMoves,
  },
  {
    title: "every param a build did not prerender is answered with the route's one fallback shell",
    run: assertParamsShareTheFallbackShell,
  },
  {
    title:
      "revalidating a path moves the static shell of the page it names, and the old shell does not come back",
    run: assertPathRevalidationMovesTheShell,
  },
  {
    title: "revalidating a tag moves the fallback shell it is on, for every param",
    run: assertTagRevalidationMovesTheFallbackShell,
  },
];

export const nextStoreReadChecks: Check[] = [
  {
    title: "a prerender nothing has read is answered from the durable store, not the function",
    run: async (ctx) => {
      const { res, html } = await page(ctx, "/cold");
      assert.equal(
        res.headers.get(ENTRY_SOURCE_HEADER),
        "store",
        "the first read of a prerendered page came from the function",
      );
      assert.equal(marker(html, "cold:static"), "prerendered at build");
    },
  },
];
