import assert from "node:assert/strict";
import { CACHE_HEADER, CACHED, tierOf, UNCACHED } from "../cacheHeaders";
import { until } from "../nextApp";
import type { Check, CheckContext } from "./context";

const KEPT_PATH = "/cache/public";
const FRESH_PATH = "/cache/fresh";
const WARM_TIMEOUT_MS = 30_000;

type Read = { res: Response; body: string };

async function read(ctx: CheckContext, path: string): Promise<Read> {
  const res = await ctx.fetch(`${ctx.baseUrl}${path}`);
  assert.equal(res.status, 200, `GET ${path} answered ${res.status}`);
  return { res, body: await res.text() };
}

export const workersCacheChecks: Check[] = [
  {
    title: "a response the container marks public is answered again from the worker's cache",
    run: async (ctx) => {
      const first = await read(ctx, KEPT_PATH);
      const kept = await until(
        WARM_TIMEOUT_MS,
        `GET ${KEPT_PATH} was never answered from the worker's cache`,
        async () => {
          const again = await read(ctx, KEPT_PATH);
          return CACHED.includes(tierOf(again.res)) ? again : undefined;
        },
      );
      assert.equal(kept.body, first.body, `the cached ${KEPT_PATH} is not the page first served`);
      assert.equal(kept.res.headers.get("cache-tag"), null, "a visitor was sent the cache's tags");
    },
  },
  {
    title: "a response the container sets no cache headers on is never kept by the worker",
    run: async (ctx) => {
      const first = await read(ctx, FRESH_PATH);
      const second = await read(ctx, FRESH_PATH);
      assert.ok(
        UNCACHED.includes(tierOf(second.res)),
        `${FRESH_PATH} was stamped ${CACHE_HEADER}: ${tierOf(second.res)}`,
      );
      assert.notEqual(second.body, first.body, `${FRESH_PATH} was served twice from one render`);
    },
  },
];
