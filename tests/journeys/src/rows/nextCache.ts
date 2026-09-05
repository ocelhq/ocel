import assert from "node:assert/strict";
import {
  CACHE_HEADER,
  CACHED,
  cacheControlFor,
  DYNAMIC_CACHE_CONTROL,
  imageCacheControl,
  IMMUTABLE_CACHE_CONTROL,
  ROUTER_VARY,
  type Tier,
  tierOf,
  UNCACHED,
  variesOn,
} from "../cacheHeaders";
import type { ContractContext, ContractRow } from "../contract";
import { assetPath, marker, markerOrNone, stamp } from "../html";
import { page, state, steady, until } from "../nextApp";

const ISR_SECONDS = 15;
const PATH_SECONDS = 3600;
const REVALIDATION_TIMEOUT_MS = 60_000;
const SETTLED_READS = 3;
const SETTLE_ATTEMPTS = 3;
const IMAGE_TTL_SECONDS = 60;
const RESUME_TAG = "résumé";
const LOCAL_IMAGE = "/ocel.png";
const ALLOWED_WIDTH = 640;
const ALLOWED_QUALITY = 75;
const DEPLOYMENT_NOTE = "next-cache:deployment";

export const EDGE_ISR_TITLE = "an edge-runtime page with a revalidate serves a cached tier";

function tierIs(res: Response, allowed: Tier[], what: string) {
  const tier = tierOf(res);
  assert.ok(allowed.includes(tier), `${what} was stamped ${CACHE_HEADER}: ${tier}`);
}

function cacheControlIs(res: Response, expected: string, what: string) {
  assert.equal(res.headers.get("cache-control"), expected, `${what} carried the wrong cache-control`);
}

async function cachedHalf(ctx: ContractContext, path: string, scope: string): Promise<string> {
  return marker((await page(ctx, path)).html, `${scope}:cached`);
}

async function settled(ctx: ContractContext, path: string, scope: string): Promise<string> {
  return steady(
    () => cachedHalf(ctx, path, scope),
    path,
    SETTLED_READS,
    SETTLE_ATTEMPTS,
  );
}

async function movedOn(
  ctx: ContractContext,
  path: string,
  scope: string,
  from: string,
): Promise<string> {
  return until(REVALIDATION_TIMEOUT_MS, `${path} never moved off ${from}`, async () => {
    const now = await cachedHalf(ctx, path, scope);
    return now === from ? undefined : now;
  });
}

async function revalidate(ctx: ContractContext, query: string): Promise<void> {
  const res = await ctx.fetch(`${ctx.baseUrl}/api/next/revalidate?${query}`, { method: "POST" });
  assert.equal(res.status, 200, `revalidating with ${query} answered ${res.status}`);
  await res.arrayBuffer();
}

function imageUrl(ctx: ContractContext, url: string, width: number, quality: number): string {
  return `${ctx.baseUrl}/_next/image?url=${encodeURIComponent(url)}&w=${width}&q=${quality}`;
}

export const nextCacheRows: ContractRow[] = [
  {
    title: "a static page is prerendered, frozen, and links assets immutable for a year",
    run: async (ctx) => {
      const { res, html } = await page(ctx, "/cache/static");
      tierIs(res, CACHED, "the static page");
      cacheControlIs(res, cacheControlFor(false), "the static page");
      assert.equal(
        markerOrNone(html, "static:live"),
        undefined,
        "the static page rendered a live half, so freezing it proves nothing",
      );
      const frozen = marker(html, "static:cached");
      assert.equal(await settled(ctx, "/cache/static", "static"), frozen);

      const asset = await ctx.fetch(`${ctx.baseUrl}${assetPath(html)}`);
      assert.equal(asset.status, 200);
      cacheControlIs(asset, IMMUTABLE_CACHE_CONTROL, "the hashed asset");
      await asset.arrayBuffer();
    },
  },
  {
    title: "an ISR page is frozen inside its revalidate window and moves once it passes",
    run: async (ctx) => {
      const { res } = await page(ctx, "/cache/isr");
      tierIs(res, CACHED, "the ISR page");
      cacheControlIs(res, cacheControlFor(ISR_SECONDS), "the ISR page");
      const before = await settled(ctx, "/cache/isr", "isr");
      await movedOn(ctx, "/cache/isr", "isr", before);
    },
  },
  {
    title: "revalidating a path moves the page it names and nothing else",
    run: async (ctx) => {
      const { res } = await page(ctx, "/cache/path");
      tierIs(res, CACHED, "the path page");
      cacheControlIs(res, cacheControlFor(PATH_SECONDS), "the path page");

      const before = await settled(ctx, "/cache/path", "path");
      const untouched = await cachedHalf(ctx, "/cache/static", "static");
      await revalidate(ctx, `path=${encodeURIComponent("/cache/path")}`);
      await movedOn(ctx, "/cache/path", "path", before);
      assert.equal(
        await cachedHalf(ctx, "/cache/static", "static"),
        untouched,
        "revalidating one path moved a page it does not name",
      );
    },
  },
  {
    title: "a dynamic page moves on every request and is never stored",
    run: async (ctx) => {
      const first = await page(ctx, "/cache/dynamic");
      tierIs(first.res, UNCACHED, "the dynamic page");
      cacheControlIs(first.res, DYNAMIC_CACHE_CONTROL, "the dynamic page");
      const second = await page(ctx, "/cache/dynamic");
      assert.notEqual(
        stamp(first.html, "dynamic").live,
        stamp(second.html, "dynamic").live,
        "the dynamic page answered twice with the same render",
      );
    },
  },
  {
    title: "an RSC request answers text/x-component, varies on the router headers and names this deployment",
    run: async (ctx) => {
      const { html } = await page(ctx, "/cache/deployment");
      const id = marker(html, "deployment");
      assert.ok(id.length > 0, "the page rendered no deployment id");

      const rsc = await ctx.fetch(`${ctx.baseUrl}/cache/deployment`, { headers: { RSC: "1" } });
      assert.equal(rsc.status, 200);
      assert.match(rsc.headers.get("content-type") ?? "", /^text\/x-component/);
      const vary = rsc.headers.get("vary");
      assert.ok(variesOn(vary, ROUTER_VARY), `the RSC response varied on ${vary}`);
      cacheControlIs(rsc, DYNAMIC_CACHE_CONTROL, "the RSC response");
      await rsc.arrayBuffer();

      const before = ctx.notes.get(DEPLOYMENT_NOTE);
      if (ctx.leg === "redeploy" && before) {
        assert.notEqual(id, before, "the redeploy served the deployment the first leg did");
      }
      ctx.notes.set(DEPLOYMENT_NOTE, id);
    },
  },
  {
    title: "a prefetch answers byte-identically to the request that is not one",
    run: async (ctx) => {
      const read = async (headers: Record<string, string>) => {
        const res = await ctx.fetch(`${ctx.baseUrl}/cache/static`, { headers });
        assert.equal(res.status, 200);
        cacheControlIs(res, cacheControlFor(false), "the flight response");
        return Buffer.from(await res.arrayBuffer());
      };
      const prefetched = await read({ RSC: "1", "Next-Router-Prefetch": "1" });
      const plain = await read({ RSC: "1" });
      assert.ok(prefetched.equals(plain), "the prefetch and the plain flight response differ");
    },
  },
  {
    title: "the image optimizer serves a local and a self-hosted image and refuses a bad host, width or quality",
    run: async (ctx) => {
      for (const url of [LOCAL_IMAGE, `${ctx.baseUrl}${LOCAL_IMAGE}`]) {
        const res = await ctx.fetch(imageUrl(ctx, url, ALLOWED_WIDTH, ALLOWED_QUALITY));
        assert.equal(res.status, 200, `${url} answered ${res.status}`);
        assert.match(res.headers.get("content-type") ?? "", /^image\//);
        cacheControlIs(res, imageCacheControl(IMAGE_TTL_SECONDS), `the optimized ${url}`);
        await res.arrayBuffer();
      }

      const refused: Array<[string, string]> = [
        ["a disallowed host", imageUrl(ctx, "https://images.invalid/ocel.png", ALLOWED_WIDTH, ALLOWED_QUALITY)],
        ["a disallowed width", imageUrl(ctx, LOCAL_IMAGE, 999, ALLOWED_QUALITY)],
        ["a disallowed quality", imageUrl(ctx, LOCAL_IMAGE, ALLOWED_WIDTH, 50)],
      ];
      for (const [what, url] of refused) {
        const res = await ctx.fetch(url);
        assert.equal(res.status, 400, `${what} answered ${res.status}`);
        await res.arrayBuffer();
      }
    },
  },
  {
    title: "draft mode bypasses the cache with a cookie that survives the redirect",
    run: async (ctx) => {
      const prerendered = await page(ctx, "/draft");
      tierIs(prerendered.res, CACHED, "the draft page without the cookie");
      assert.equal(marker(prerendered.html, "draft"), "disabled");

      const turned = await ctx.fetch(`${ctx.baseUrl}/draft/enable`, { redirect: "manual" });
      assert.equal(turned.status, 307, `enabling draft mode answered ${turned.status}`);
      await turned.arrayBuffer();
      const setCookie = turned.headers.get("set-cookie") ?? "";
      assert.match(setCookie, /__prerender_bypass=/, "the 307 carried no draft cookie");
      const cookie = setCookie.split(";")[0]!;
      assert.equal(new URL(turned.headers.get("location") ?? "", ctx.baseUrl).pathname, "/draft");

      const drafted = await page(ctx, "/draft", { headers: { cookie } });
      assert.equal(tierOf(drafted.res), "BYPASS");
      cacheControlIs(drafted.res, DYNAMIC_CACHE_CONTROL, "the drafted page");
      assert.equal(marker(drafted.html, "draft"), "enabled");
    },
  },
  {
    title: EDGE_ISR_TITLE,
    run: async (ctx) => {
      const { res } = await page(ctx, "/cache/edge");
      tierIs(res, CACHED, "the edge-runtime page");
      cacheControlIs(res, cacheControlFor(ISR_SECONDS), "the edge-runtime page");
      const before = await settled(ctx, "/cache/edge", "edge");
      await movedOn(ctx, "/cache/edge", "edge", before);
    },
  },
];

export const nextDataCacheRows: ContractRow[] = [
  {
    title: "a non-ASCII tag holds one upstream call and releases it when the tag is revalidated",
    run: async (ctx) => {
      const { res, html } = await page(ctx, "/cache/data");
      tierIs(res, UNCACHED, "the data-cache page");
      cacheControlIs(res, DYNAMIC_CACHE_CONTROL, "the data-cache page");

      const held = await settled(ctx, "/cache/data", "data");
      const again = stamp((await page(ctx, "/cache/data")).html, "data");
      assert.equal(again.cached, held, "the tag released the upstream call it was holding");
      assert.notEqual(
        again.live,
        stamp(html, "data").live,
        "the data-cache page answered twice with the same render",
      );

      const counted = (await state(ctx, ["upstream:data"])).get("upstream:data");
      assert.ok(counted, "the upstream never reached the state readback");
      assert.equal(
        String(counted.count),
        held,
        "the page and the readback disagree on how often the upstream was called",
      );

      await revalidate(ctx, `tag=${encodeURIComponent(RESUME_TAG)}`);
      const after = await movedOn(ctx, "/cache/data", "data", held);
      assert.ok(Number(after) > Number(held), `the upstream count went from ${held} to ${after}`);
    },
  },
];
