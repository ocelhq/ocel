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

function tierIs(res: Response, allowed: Tier[], what: string) {
  const tier = tierOf(res);
  assert.ok(allowed.includes(tier), `${what} was stamped ${CACHE_HEADER}: ${tier}`);
}

function servedFromNextCache(res: Response, what: string) {
  const tier = nextServerTierOf(res);
  assert.ok(
    tier !== undefined && NEXT_SERVER_CACHED.includes(tier),
    `${what} was stamped ${NEXT_CACHE_HEADER}: ${tier}, not served from the Next server's cache`,
  );
}

function renderedOutsideNextCache(res: Response, what: string) {
  const tier = nextServerTierOf(res);
  assert.equal(tier, undefined, `${what} was stamped ${NEXT_CACHE_HEADER}: ${tier}`);
}

function heldAt(cacheLayer: CacheLayer, checks: Check[]): Check[] {
  return checks.map((one) => ({ ...one, cacheLayer }));
}

function cacheControlIs(res: Response, expected: string, what: string) {
  const cacheControl = res.headers.get("cache-control");
  assert.ok(
    sameDirectives(cacheControl, expected),
    `${what} sent cache-control ${cacheControl}, not the directives of ${expected}`,
  );
}

async function cachedHalf(ctx: CheckContext, path: string, scope: string): Promise<string> {
  return marker((await page(ctx, path)).html, `${scope}:cached`);
}

async function steadyCachedHalf(ctx: CheckContext, path: string, scope: string): Promise<string> {
  return steady(() => cachedHalf(ctx, path, scope), path, STEADY_READS, STEADY_ATTEMPTS);
}

async function movedOn(
  ctx: CheckContext,
  path: string,
  scope: string,
  from: string,
): Promise<string> {
  return until(REVALIDATION_TIMEOUT_MS, `${path} never moved off ${from}`, async () => {
    const now = await cachedHalf(ctx, path, scope);
    return now === from ? undefined : now;
  });
}

async function revalidate(ctx: CheckContext, query: string): Promise<void> {
  const res = await ctx.fetch(`${ctx.baseUrl}/api/next/revalidate?${query}`, { method: "POST" });
  assert.equal(res.status, 200, `revalidating with ${query} answered ${res.status}`);
  await res.arrayBuffer();
}

function imageUrl(ctx: CheckContext, url: string, width: number, quality: number): string {
  return `${ctx.baseUrl}/_next/image?url=${encodeURIComponent(url)}&w=${width}&q=${quality}`;
}

async function staticPageFrozen(ctx: CheckContext, html: string): Promise<void> {
  assert.equal(
    markerOrNone(html, "static:live"),
    undefined,
    "the static page rendered a live half, so freezing it proves nothing",
  );
  const frozen = marker(html, "static:cached");
  assert.equal(await steadyCachedHalf(ctx, "/cache/static", "static"), frozen);

  const asset = await ctx.fetch(`${ctx.baseUrl}${assetPath(html)}`);
  assert.equal(asset.status, 200);
  cacheControlIs(asset, IMMUTABLE_CACHE_CONTROL, "the hashed asset");
  await asset.arrayBuffer();
}

async function isrPageMoves(ctx: CheckContext): Promise<void> {
  const before = await steadyCachedHalf(ctx, "/cache/isr", "isr");
  await movedOn(ctx, "/cache/isr", "isr", before);
}

async function pathRevalidationMovesOnlyThatPage(ctx: CheckContext): Promise<void> {
  const before = await steadyCachedHalf(ctx, "/cache/path", "path");
  const untouched = await cachedHalf(ctx, "/cache/static", "static");
  await revalidate(ctx, `path=${encodeURIComponent("/cache/path")}`);
  await movedOn(ctx, "/cache/path", "path", before);
  assert.equal(
    await cachedHalf(ctx, "/cache/static", "static"),
    untouched,
    "revalidating one path moved a page it does not name",
  );
}

async function dynamicPageMoves(
  ctx: CheckContext,
  first: { res: Response; html: string },
): Promise<void> {
  cacheControlIs(first.res, DYNAMIC_CACHE_CONTROL, "the dynamic page");
  const second = await page(ctx, "/cache/dynamic");
  assert.notEqual(
    stamp(first.html, "dynamic").live,
    stamp(second.html, "dynamic").live,
    "the dynamic page answered twice with the same render",
  );
}

async function rscAnswer(ctx: CheckContext): Promise<void> {
  const rsc = await ctx.fetch(`${ctx.baseUrl}/cache/deployment`, { headers: { RSC: "1" } });
  assert.equal(rsc.status, 200);
  assert.match(rsc.headers.get("content-type") ?? "", /^text\/x-component/);
  const vary = rsc.headers.get("vary");
  assert.ok(variesOn(vary, ROUTER_VARY), `the RSC response varied on ${vary}`);
  cacheControlIs(rsc, DYNAMIC_CACHE_CONTROL, "the RSC response");
  await rsc.arrayBuffer();
}

async function prefetchMatchesFlight(ctx: CheckContext, cacheControl: string): Promise<void> {
  const read = async (headers: Record<string, string>) => {
    const res = await ctx.fetch(`${ctx.baseUrl}/cache/static`, { headers });
    assert.equal(res.status, 200);
    cacheControlIs(res, cacheControl, "the flight response");
    return Buffer.from(await res.arrayBuffer());
  };
  const prefetched = await read({ RSC: "1", "Next-Router-Prefetch": "1" });
  const plain = await read({ RSC: "1" });
  assert.ok(prefetched.equals(plain), "the prefetch and the plain flight response differ");
}

async function imageOptimizerServes(ctx: CheckContext, urls: string[]): Promise<void> {
  for (const url of urls) {
    const res = await ctx.fetch(imageUrl(ctx, url, ALLOWED_WIDTH, ALLOWED_QUALITY));
    assert.equal(res.status, 200, `${url} answered ${res.status}`);
    assert.match(res.headers.get("content-type") ?? "", /^image\//);
    cacheControlIs(res, imageCacheControl(IMAGE_TTL_SECONDS), `the optimized ${url}`);
    await res.arrayBuffer();
  }

  const refused: Array<[string, string]> = [
    [
      "a disallowed host",
      imageUrl(ctx, "https://images.invalid/ocel.png", ALLOWED_WIDTH, ALLOWED_QUALITY),
    ],
    ["a disallowed width", imageUrl(ctx, LOCAL_IMAGE, 999, ALLOWED_QUALITY)],
    ["a disallowed quality", imageUrl(ctx, LOCAL_IMAGE, ALLOWED_WIDTH, 50)],
  ];
  for (const [what, url] of refused) {
    const res = await ctx.fetch(url);
    assert.equal(res.status, 400, `${what} answered ${res.status}`);
    await res.arrayBuffer();
  }
}

async function draftModeBypasses(
  ctx: CheckContext,
  prerendered: (res: Response) => void,
  drafted: (res: Response) => void,
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
  drafted(withCookie.res);
  cacheControlIs(withCookie.res, DYNAMIC_CACHE_CONTROL, "the drafted page");
  assert.equal(marker(withCookie.html, "draft"), "enabled");
}

async function tagCachesUpstream(ctx: CheckContext, html: string): Promise<void> {
  const cachedCount = await steadyCachedHalf(ctx, "/cache/data", "data");
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
  const after = await movedOn(ctx, "/cache/data", "data", cachedCount);
  assert.ok(
    Number(after) > Number(cachedCount),
    `the upstream count went from ${cachedCount} to ${after}`,
  );
}

export const nextCacheChecks: Check[] = heldAt("edge", [
  {
    title: "a static page is prerendered, frozen, and links assets immutable for a year",
    run: async (ctx) => {
      const { res, html } = await page(ctx, "/cache/static");
      tierIs(res, CACHED, "the static page");
      cacheControlIs(res, cacheControlFor(false), "the static page");
      await staticPageFrozen(ctx, html);
    },
  },
  {
    title: "an ISR page is frozen inside its revalidate window and moves once it passes",
    run: async (ctx) => {
      const { res } = await page(ctx, "/cache/isr");
      tierIs(res, CACHED, "the ISR page");
      cacheControlIs(res, cacheControlFor(ISR_SECONDS), "the ISR page");
      await isrPageMoves(ctx);
    },
  },
  {
    title: "revalidating a path moves the page it names and nothing else",
    run: async (ctx) => {
      const { res } = await page(ctx, "/cache/path");
      tierIs(res, CACHED, "the path page");
      cacheControlIs(res, cacheControlFor(PATH_SECONDS), "the path page");
      await pathRevalidationMovesOnlyThatPage(ctx);
    },
  },
  {
    title: "a dynamic page moves on every request and is never stored",
    run: async (ctx) => {
      const first = await page(ctx, "/cache/dynamic");
      tierIs(first.res, UNCACHED, "the dynamic page");
      await dynamicPageMoves(ctx, first);
    },
  },
  {
    title:
      "an RSC request answers text/x-component, varies on the router headers and names this deployment",
    run: async (ctx) => {
      const { html } = await page(ctx, "/cache/deployment");
      const id = marker(html, "deployment");
      assert.ok(id.length > 0, "the page rendered no deployment id");
      await rscAnswer(ctx);

      const before = ctx.notes.get(DEPLOYMENT_NOTE);
      if (ctx.phase === "redeploy" && before) {
        assert.notEqual(id, before, "the redeploy served the deployment the first deploy did");
      }
      ctx.notes.set(DEPLOYMENT_NOTE, id);
    },
  },
  {
    title: "a prefetch answers byte-identically to the request that is not one",
    run: (ctx) => prefetchMatchesFlight(ctx, cacheControlFor(false)),
  },
  {
    title:
      "the image optimizer serves a local and a self-hosted image and refuses a bad host, width or quality",
    run: (ctx) => imageOptimizerServes(ctx, [LOCAL_IMAGE, `${ctx.baseUrl}${LOCAL_IMAGE}`]),
  },
  {
    title: "draft mode bypasses the cache with a cookie that survives the redirect",
    run: (ctx) =>
      draftModeBypasses(
        ctx,
        (res) => tierIs(res, CACHED, "the draft page without the cookie"),
        (res) => assert.equal(tierOf(res), "BYPASS"),
      ),
  },
]);

export const nextDataCacheChecks: Check[] = heldAt("edge", [
  {
    title: "a non-ASCII tag caches one upstream call and releases it when the tag is revalidated",
    run: async (ctx) => {
      const { res, html } = await page(ctx, "/cache/data");
      tierIs(res, UNCACHED, "the data-cache page");
      cacheControlIs(res, DYNAMIC_CACHE_CONTROL, "the data-cache page");
      await tagCachesUpstream(ctx, html);
    },
  },
]);

export const nextOriginCacheChecks: Check[] = heldAt("origin", [
  {
    title:
      "the Next server serves a static page from its cache, frozen, with assets immutable for a year",
    run: async (ctx) => {
      const { res, html } = await page(ctx, "/cache/static");
      servedFromNextCache(res, "the static page");
      cacheControlIs(res, nextServerCacheControlFor(false), "the static page");
      await staticPageFrozen(ctx, html);
    },
  },
  {
    title:
      "the Next server keeps an ISR page frozen inside its revalidate window and moves it after",
    run: async (ctx) => {
      const { res } = await page(ctx, "/cache/isr");
      servedFromNextCache(res, "the ISR page");
      cacheControlIs(res, nextServerCacheControlFor(ISR_SECONDS), "the ISR page");
      await isrPageMoves(ctx);
    },
  },
  {
    title: "revalidating a path makes the Next server move the page it names and nothing else",
    run: async (ctx) => {
      const { res } = await page(ctx, "/cache/path");
      servedFromNextCache(res, "the path page");
      cacheControlIs(res, nextServerCacheControlFor(PATH_SECONDS), "the path page");
      await pathRevalidationMovesOnlyThatPage(ctx);
    },
  },
  {
    title: "the Next server renders a dynamic page on every request and keeps none of it",
    run: async (ctx) => {
      const first = await page(ctx, "/cache/dynamic");
      renderedOutsideNextCache(first.res, "the dynamic page");
      await dynamicPageMoves(ctx, first);
    },
  },
  {
    title:
      "the Next server answers an RSC request as text/x-component that varies on the router headers",
    run: rscAnswer,
  },
  {
    title: "the Next server answers a prefetch byte-identically to the request that is not one",
    run: (ctx) => prefetchMatchesFlight(ctx, nextServerCacheControlFor(false)),
  },
  {
    title:
      "the Next server's image optimizer serves a local image and refuses a bad host, width or quality",
    run: (ctx) => imageOptimizerServes(ctx, [LOCAL_IMAGE]),
  },
  {
    title:
      "draft mode makes the Next server bypass its cache with a cookie that survives the redirect",
    run: (ctx) =>
      draftModeBypasses(
        ctx,
        (res) => servedFromNextCache(res, "the draft page without the cookie"),
        () => undefined,
      ),
  },
]);

export const nextOriginDataCacheChecks: Check[] = heldAt("origin", [
  {
    title:
      "the Next server caches one upstream call under a non-ASCII tag and releases it when the tag is revalidated",
    run: async (ctx) => {
      const { res, html } = await page(ctx, "/cache/data");
      renderedOutsideNextCache(res, "the data-cache page");
      cacheControlIs(res, DYNAMIC_CACHE_CONTROL, "the data-cache page");
      await tagCachesUpstream(ctx, html);
    },
  },
]);
