import assert from "node:assert/strict";
import {
  CACHE_HEADER,
  CACHED,
  cacheControlFor,
  DYNAMIC_CACHE_CONTROL,
  IMMUTABLE_CACHE_CONTROL,
  imageCacheControl,
  NEXT_CACHE_HEADER,
  NEXT_SERVER_CACHED,
  nextServerCacheControlFor,
  nextServerTierOf,
  ROUTER_VARY,
  sameDirectives,
  type Tier,
  tierOf,
  UNCACHED,
  variesOn,
} from "../cacheHeaders";
import { assetPath, marker, markerOrNone, stamp } from "../html";
import type { CacheLayer } from "../matrix/types";
import { page, state, steady, until } from "../nextApp";
import type { Check, CheckContext } from "./context";

const ISR_SECONDS = 15;
const PATH_SECONDS = 3600;
const REVALIDATION_TIMEOUT_MS = 60_000;
const STEADY_READS = 3;
const STEADY_ATTEMPTS = 3;
const IMAGE_TTL_SECONDS = 60;
const RESUME_TAG = "résumé";
const LOCAL_IMAGE = "/ocel.png";
const ALLOWED_WIDTH = 640;
const ALLOWED_QUALITY = 75;
const DEPLOYMENT_NOTE = "next-cache:deployment";

function assertTier(res: Response, allowed: Tier[], what: string) {
  const tier = tierOf(res);
  assert.ok(allowed.includes(tier), `${what} was stamped ${CACHE_HEADER}: ${tier}`);
}

function assertServedFromNextCache(res: Response, what: string) {
  const tier = nextServerTierOf(res);
  assert.ok(
    tier !== undefined && NEXT_SERVER_CACHED.includes(tier),
    `${what} was stamped ${NEXT_CACHE_HEADER}: ${tier}, not served from the Next server's cache`,
  );
}

function assertRenderedOutsideNextCache(res: Response, what: string) {
  const tier = nextServerTierOf(res);
  assert.equal(tier, undefined, `${what} was stamped ${NEXT_CACHE_HEADER}: ${tier}`);
}

function holdAt(cacheLayer: CacheLayer, checks: Check[]): Check[] {
  return checks.map((one) => ({ ...one, cacheLayer }));
}

function assertCacheControl(res: Response, expected: string, what: string) {
  const cacheControl = res.headers.get("cache-control");
  assert.ok(
    sameDirectives(cacheControl, expected),
    `${what} sent cache-control ${cacheControl}, not the directives of ${expected}`,
  );
}

async function readCachedHalf(ctx: CheckContext, path: string, scope: string): Promise<string> {
  return marker((await page(ctx, path)).html, `${scope}:cached`);
}

async function readSteadyCachedHalf(
  ctx: CheckContext,
  path: string,
  scope: string,
): Promise<string> {
  return steady(() => readCachedHalf(ctx, path, scope), path, STEADY_READS, STEADY_ATTEMPTS);
}

async function waitForNewCachedHalf(
  ctx: CheckContext,
  path: string,
  scope: string,
  from: string,
): Promise<string> {
  return until(REVALIDATION_TIMEOUT_MS, `${path} never moved off ${from}`, async () => {
    const now = await readCachedHalf(ctx, path, scope);
    return now === from ? undefined : now;
  });
}

async function revalidate(ctx: CheckContext, query: string): Promise<void> {
  const res = await ctx.fetch(`${ctx.baseUrl}/api/next/revalidate?${query}`, { method: "POST" });
  assert.equal(res.status, 200, `revalidating with ${query} answered ${res.status}`);
  await res.arrayBuffer();
}

function buildImageUrl(ctx: CheckContext, url: string, width: number, quality: number): string {
  return `${ctx.baseUrl}/_next/image?url=${encodeURIComponent(url)}&w=${width}&q=${quality}`;
}

async function assertStaticPageFrozen(ctx: CheckContext, html: string): Promise<void> {
  assert.equal(
    markerOrNone(html, "static:live"),
    undefined,
    "the static page rendered a live half, so freezing it proves nothing",
  );
  const frozen = marker(html, "static:cached");
  assert.equal(await readSteadyCachedHalf(ctx, "/cache/static", "static"), frozen);

  const asset = await ctx.fetch(`${ctx.baseUrl}${assetPath(html)}`);
  assert.equal(asset.status, 200);
  assertCacheControl(asset, IMMUTABLE_CACHE_CONTROL, "the hashed asset");
  await asset.arrayBuffer();
}

async function assertIsrPageMoves(ctx: CheckContext): Promise<void> {
  const before = await readSteadyCachedHalf(ctx, "/cache/isr", "isr");
  await waitForNewCachedHalf(ctx, "/cache/isr", "isr", before);
}

async function assertPathRevalidationMovesOnlyThatPage(ctx: CheckContext): Promise<void> {
  const before = await readSteadyCachedHalf(ctx, "/cache/path", "path");
  const untouched = await readCachedHalf(ctx, "/cache/static", "static");
  await revalidate(ctx, `path=${encodeURIComponent("/cache/path")}`);
  await waitForNewCachedHalf(ctx, "/cache/path", "path", before);
  assert.equal(
    await readCachedHalf(ctx, "/cache/static", "static"),
    untouched,
    "revalidating one path moved a page it does not name",
  );
}

async function assertDynamicPageMoves(
  ctx: CheckContext,
  first: { res: Response; html: string },
): Promise<void> {
  assertCacheControl(first.res, DYNAMIC_CACHE_CONTROL, "the dynamic page");
  const second = await page(ctx, "/cache/dynamic");
  assert.notEqual(
    stamp(first.html, "dynamic").live,
    stamp(second.html, "dynamic").live,
    "the dynamic page answered twice with the same render",
  );
}

async function assertRscAnswer(ctx: CheckContext): Promise<void> {
  const rsc = await ctx.fetch(`${ctx.baseUrl}/cache/deployment`, { headers: { RSC: "1" } });
  assert.equal(rsc.status, 200);
  assert.match(rsc.headers.get("content-type") ?? "", /^text\/x-component/);
  const vary = rsc.headers.get("vary");
  assert.ok(variesOn(vary, ROUTER_VARY), `the RSC response varied on ${vary}`);
  assertCacheControl(rsc, DYNAMIC_CACHE_CONTROL, "the RSC response");
  await rsc.arrayBuffer();
}

async function assertPrefetchMatchesFlight(ctx: CheckContext, cacheControl: string): Promise<void> {
  const read = async (headers: Record<string, string>) => {
    const res = await ctx.fetch(`${ctx.baseUrl}/cache/static`, { headers });
    assert.equal(res.status, 200);
    assertCacheControl(res, cacheControl, "the flight response");
    return Buffer.from(await res.arrayBuffer());
  };
  const prefetched = await read({ RSC: "1", "Next-Router-Prefetch": "1" });
  const plain = await read({ RSC: "1" });
  assert.ok(prefetched.equals(plain), "the prefetch and the plain flight response differ");
}

async function assertImageOptimizerServes(ctx: CheckContext, urls: string[]): Promise<void> {
  for (const url of urls) {
    const res = await ctx.fetch(buildImageUrl(ctx, url, ALLOWED_WIDTH, ALLOWED_QUALITY));
    assert.equal(res.status, 200, `${url} answered ${res.status}`);
    assert.match(res.headers.get("content-type") ?? "", /^image\//);
    assertCacheControl(res, imageCacheControl(IMAGE_TTL_SECONDS), `the optimized ${url}`);
    await res.arrayBuffer();
  }

  const refused: Array<[string, string]> = [
    [
      "a disallowed host",
      buildImageUrl(ctx, "https://images.invalid/ocel.png", ALLOWED_WIDTH, ALLOWED_QUALITY),
    ],
    ["a disallowed width", buildImageUrl(ctx, LOCAL_IMAGE, 999, ALLOWED_QUALITY)],
    ["a disallowed quality", buildImageUrl(ctx, LOCAL_IMAGE, ALLOWED_WIDTH, 50)],
  ];
  for (const [what, url] of refused) {
    const res = await ctx.fetch(url);
    assert.equal(res.status, 400, `${what} answered ${res.status}`);
    await res.arrayBuffer();
  }
}

async function assertDraftModeBypasses(
  ctx: CheckContext,
  prerendered: (res: Response) => void,
  drafted?: (res: Response) => void,
): Promise<void> {
  const withoutCookie = await page(ctx, "/draft");
  prerendered(withoutCookie.res);
  assert.equal(marker(withoutCookie.html, "draft"), "disabled");

  const turned = await ctx.fetch(`${ctx.baseUrl}/draft/enable`, { redirect: "manual" });
  assert.equal(turned.status, 307, `enabling draft mode answered ${turned.status}`);
  await turned.arrayBuffer();
  const setCookie = turned.headers.get("set-cookie") ?? "";
  assert.match(setCookie, /__prerender_bypass=/, "the 307 sent no draft cookie");
  const cookie = setCookie.split(";")[0]!;
  assert.equal(new URL(turned.headers.get("location") ?? "", ctx.baseUrl).pathname, "/draft");

  const withCookie = await page(ctx, "/draft", { headers: { cookie } });
  drafted?.(withCookie.res);
  assertCacheControl(withCookie.res, DYNAMIC_CACHE_CONTROL, "the drafted page");
  assert.equal(marker(withCookie.html, "draft"), "enabled");
}

async function assertTagCachesUpstream(ctx: CheckContext, html: string): Promise<void> {
  const cachedCount = await readSteadyCachedHalf(ctx, "/cache/data", "data");
  const again = stamp((await page(ctx, "/cache/data")).html, "data");
  assert.equal(again.cached, cachedCount, "the tag released the upstream call it was caching");
  assert.notEqual(
    again.live,
    stamp(html, "data").live,
    "the data-cache page answered twice with the same render",
  );

  const counted = (await state(ctx, ["upstream:data"])).get("upstream:data");
  assert.ok(counted, "the upstream never reached the state readback");
  assert.equal(
    String(counted.count),
    cachedCount,
    "the page and the readback disagree on how often the upstream was called",
  );

  await revalidate(ctx, `tag=${encodeURIComponent(RESUME_TAG)}`);
  const after = await waitForNewCachedHalf(ctx, "/cache/data", "data", cachedCount);
  assert.ok(
    Number(after) > Number(cachedCount),
    `the upstream count went from ${cachedCount} to ${after}`,
  );
}

export const nextCacheChecks: Check[] = holdAt("edge", [
  {
    title: "a static page is prerendered, frozen, and links assets immutable for a year",
    run: async (ctx) => {
      const { res, html } = await page(ctx, "/cache/static");
      assertTier(res, CACHED, "the static page");
      assertCacheControl(res, cacheControlFor(false), "the static page");
      await assertStaticPageFrozen(ctx, html);
    },
  },
  {
    title: "an ISR page is frozen inside its revalidate window and moves once it passes",
    run: async (ctx) => {
      const { res } = await page(ctx, "/cache/isr");
      assertTier(res, CACHED, "the ISR page");
      assertCacheControl(res, cacheControlFor(ISR_SECONDS), "the ISR page");
      await assertIsrPageMoves(ctx);
    },
  },
  {
    title: "revalidating a path moves the page it names and nothing else",
    run: async (ctx) => {
      const { res } = await page(ctx, "/cache/path");
      assertTier(res, CACHED, "the path page");
      assertCacheControl(res, cacheControlFor(PATH_SECONDS), "the path page");
      await assertPathRevalidationMovesOnlyThatPage(ctx);
    },
  },
  {
    title: "a dynamic page moves on every request and is never stored",
    run: async (ctx) => {
      const first = await page(ctx, "/cache/dynamic");
      assertTier(first.res, UNCACHED, "the dynamic page");
      await assertDynamicPageMoves(ctx, first);
    },
  },
  {
    title:
      "an RSC request answers text/x-component, varies on the router headers and names this deployment",
    run: async (ctx) => {
      const { html } = await page(ctx, "/cache/deployment");
      const id = marker(html, "deployment");
      assert.ok(id.length > 0, "the page rendered no deployment id");
      await assertRscAnswer(ctx);

      const before = ctx.notes.get(DEPLOYMENT_NOTE);
      if (ctx.phase === "redeploy" && before) {
        assert.notEqual(id, before, "the redeploy served the deployment the first deploy did");
      }
      ctx.notes.set(DEPLOYMENT_NOTE, id);
    },
  },
  {
    title: "a prefetch answers byte-identically to the request that is not one",
    run: (ctx) => assertPrefetchMatchesFlight(ctx, cacheControlFor(false)),
  },
  {
    title:
      "the image optimizer serves a local and a self-hosted image and refuses a bad host, width or quality",
    run: (ctx) => assertImageOptimizerServes(ctx, [LOCAL_IMAGE, `${ctx.baseUrl}${LOCAL_IMAGE}`]),
  },
  {
    title: "draft mode bypasses the cache with a cookie that survives the redirect",
    run: (ctx) =>
      assertDraftModeBypasses(
        ctx,
        (res) => assertTier(res, CACHED, "the draft page without the cookie"),
        (res) => assert.equal(tierOf(res), "BYPASS"),
      ),
  },
]);

export const nextDataCacheChecks: Check[] = holdAt("edge", [
  {
    title: "a non-ASCII tag caches one upstream call and releases it when the tag is revalidated",
    run: async (ctx) => {
      const { res, html } = await page(ctx, "/cache/data");
      assertTier(res, UNCACHED, "the data-cache page");
      assertCacheControl(res, DYNAMIC_CACHE_CONTROL, "the data-cache page");
      await assertTagCachesUpstream(ctx, html);
    },
  },
]);

export const nextOriginCacheChecks: Check[] = holdAt("origin", [
  {
    title:
      "the Next server serves a static page from its cache, frozen, with assets immutable for a year",
    run: async (ctx) => {
      const { res, html } = await page(ctx, "/cache/static");
      assertServedFromNextCache(res, "the static page");
      assertCacheControl(res, nextServerCacheControlFor(false), "the static page");
      await assertStaticPageFrozen(ctx, html);
    },
  },
  {
    title:
      "the Next server keeps an ISR page frozen inside its revalidate window and moves it after",
    run: async (ctx) => {
      const { res } = await page(ctx, "/cache/isr");
      assertServedFromNextCache(res, "the ISR page");
      assertCacheControl(res, nextServerCacheControlFor(ISR_SECONDS), "the ISR page");
      await assertIsrPageMoves(ctx);
    },
  },
  {
    title: "revalidating a path makes the Next server move the page it names and nothing else",
    run: async (ctx) => {
      const { res } = await page(ctx, "/cache/path");
      assertServedFromNextCache(res, "the path page");
      assertCacheControl(res, nextServerCacheControlFor(PATH_SECONDS), "the path page");
      await assertPathRevalidationMovesOnlyThatPage(ctx);
    },
  },
  {
    title: "the Next server renders a dynamic page on every request and keeps none of it",
    run: async (ctx) => {
      const first = await page(ctx, "/cache/dynamic");
      assertRenderedOutsideNextCache(first.res, "the dynamic page");
      await assertDynamicPageMoves(ctx, first);
    },
  },
  {
    title:
      "the Next server answers an RSC request as text/x-component that varies on the router headers",
    run: assertRscAnswer,
  },
  {
    title: "the Next server answers a prefetch byte-identically to the request that is not one",
    run: (ctx) => assertPrefetchMatchesFlight(ctx, nextServerCacheControlFor(false)),
  },
  {
    title:
      "the Next server's image optimizer serves a local image and refuses a bad host, width or quality",
    run: (ctx) => assertImageOptimizerServes(ctx, [LOCAL_IMAGE]),
  },
  {
    title:
      "draft mode makes the Next server bypass its cache with a cookie that survives the redirect",
    run: (ctx) =>
      assertDraftModeBypasses(ctx, (res) =>
        assertServedFromNextCache(res, "the draft page without the cookie"),
      ),
  },
]);

export const nextOriginDataCacheChecks: Check[] = holdAt("origin", [
  {
    title:
      "the Next server caches one upstream call under a non-ASCII tag and releases it when the tag is revalidated",
    run: async (ctx) => {
      const { res, html } = await page(ctx, "/cache/data");
      assertRenderedOutsideNextCache(res, "the data-cache page");
      assertCacheControl(res, DYNAMIC_CACHE_CONTROL, "the data-cache page");
      await assertTagCachesUpstream(ctx, html);
    },
  },
]);
