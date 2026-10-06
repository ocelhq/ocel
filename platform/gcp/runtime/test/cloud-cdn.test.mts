import http from "node:http";
import type { AddressInfo } from "node:net";
import { afterEach, expect, test, vi } from "vitest";
import {
  cacheTagsForCloudCdn,
  cloudCdnRelease,
  forCloudCdn,
  trimVaryForCloudCdn,
  withholdFromCloudCdn,
  withholdResponsesFromCloudCdn,
} from "../src/next/cloud-cdn.mjs";

const release = "r1a2b3c4d";
const url = "https://shop.example/blog";

afterEach(() => {
  vi.restoreAllMocks();
});

test("a Next response's Vary keeps the headers the cache key holds and drops the router state tree", () => {
  expect(
    trimVaryForCloudCdn(
      "rsc, next-router-state-tree, next-router-prefetch, next-router-segment-prefetch",
    ),
  ).toBe("rsc, next-router-prefetch, next-router-segment-prefetch");
  expect(trimVaryForCloudCdn("RSC, Next-Router-State-Tree, Next-URL")).toBe("RSC, Next-URL");
});

test("a Vary naming only the router state tree is removed", () => {
  expect(trimVaryForCloudCdn("next-router-state-tree")).toBeNull();
  expect(trimVaryForCloudCdn(null)).toBeNull();
  const response = forCloudCdn(
    new Response("x", { headers: { vary: "next-router-state-tree" } }),
    null,
    url,
  );
  expect(response.headers.has("vary")).toBe(false);
});

test("a Vary naming the interception url keeps it, because the cache key holds it", () => {
  expect(
    trimVaryForCloudCdn(
      "rsc, next-router-state-tree, next-router-prefetch, next-router-segment-prefetch, next-url",
    ),
  ).toBe("rsc, next-router-prefetch, next-router-segment-prefetch, next-url");
});

test("a Vary naming one header twice names it once", () => {
  expect(trimVaryForCloudCdn("rsc, RSC")).toBe("rsc");
});

test("a response whose Vary needs no change is passed through untouched", () => {
  const varied = new Response("x", { headers: { vary: "accept" } });
  const plain = new Response("x");
  expect(forCloudCdn(varied, null, url)).toBe(varied);
  expect(forCloudCdn(plain, null, url)).toBe(plain);
  expect(trimVaryForCloudCdn("Accept")).toBe("Accept");
});

test("the rewritten response keeps its status, body and every other header", async () => {
  const response = forCloudCdn(
    new Response("payload", {
      status: 203,
      statusText: "Odd",
      headers: {
        vary: "rsc, next-router-state-tree",
        "x-keep": "1",
        "content-type": "text/x-component",
      },
    }),
    null,
    url,
  );

  expect(response.status).toBe(203);
  expect(response.statusText).toBe("Odd");
  expect(response.headers.get("vary")).toBe("rsc");
  expect(response.headers.get("x-keep")).toBe("1");
  expect(response.headers.get("content-type")).toBe("text/x-component");
  expect(await response.text()).toBe("payload");
});

function tagsOf(response: Response): string | null {
  return response.headers.get("cache-tag");
}

function shapedTags(count: number): string {
  return Array.from({ length: count }, (_, i) => `${release}|tag${i}`).join(",");
}

test("every response a release serves carries its release as a cache tag", () => {
  expect(cacheTagsForCloudCdn(release, null, "s-maxage=60")).toEqual({
    value: release,
    dropped: [],
  });
  expect(tagsOf(forCloudCdn(new Response("x"), release, url))).toBe(release);
});

test("a page's own tags follow its release tag", () => {
  expect(cacheTagsForCloudCdn(release, `${release}|posts,${release}|_N_T_/blog`, null).value).toBe(
    `${release},${release}|posts,${release}|_N_T_/blog`,
  );
  expect(cacheTagsForCloudCdn(release, `a, ${release} ,a,,b`, null).value).toBe(`${release},a,b`);
});

test("an immutable asset carries no release tag, so a promotion never purges a chunk old pages still load", () => {
  expect(cacheTagsForCloudCdn(release, null, "public, max-age=31536000, Immutable")).toEqual({
    value: null,
    dropped: [],
  });
});

test("a page with more tags than Cloud CDN stores keeps its release and the first forty-nine", () => {
  const { value, dropped } = cacheTagsForCloudCdn(release, shapedTags(60), null);

  expect(value?.split(",")).toEqual([
    release,
    ...Array.from({ length: 49 }, (_, i) => `${release}|tag${i}`),
  ]);
  expect(dropped).toHaveLength(11);
});

test("a tag longer than Cloud CDN stores is dropped and the rest are kept", () => {
  const long = "x".repeat(121);
  const multibyte = "é".repeat(61);

  expect(cacheTagsForCloudCdn(null, `a,${long},${multibyte},b`, null)).toEqual({
    value: "a,b",
    dropped: [long, multibyte],
  });
});

test("tags past four kilobytes are dropped, so the response stays cacheable", () => {
  const tags = Array.from(
    { length: 40 },
    (_, i) => `${String(i).padStart(3, "0")}${"y".repeat(110)}`,
  );

  const { value, dropped } = cacheTagsForCloudCdn(null, tags.join(","), null);

  expect(Buffer.byteLength(value!)).toBeLessThanOrEqual(4096);
  expect(value!.split(",")).toEqual(tags.slice(0, 35));
  expect(dropped).toEqual(tags.slice(35));
});

test("a service told no release adds no tag", () => {
  expect(cacheTagsForCloudCdn(null, "a,b", null)).toEqual({ value: "a,b", dropped: [] });
  const bare = new Response("x");
  expect(forCloudCdn(bare, null, url)).toBe(bare);
});

test("a service whose edge purges nothing by tag is told no release", () => {
  expect(cloudCdnRelease({ OCEL_ISR_PREFIX: `production/shop/web/${release}/isr` })).toBeNull();
  expect(
    cloudCdnRelease({
      OCEL_CACHE_TAG_PURGE: "1",
      OCEL_ISR_PREFIX: `production/shop/web/${release}/isr`,
    }),
  ).toBe(release);
});

test("tags dropped past Cloud CDN's limits are named in one warning", () => {
  const warn = vi.spyOn(console, "warn").mockImplementation(() => {});

  forCloudCdn(new Response("x", { headers: { "cache-tag": shapedTags(60) } }), release, url);

  expect(warn).toHaveBeenCalledTimes(1);
  expect(warn.mock.calls[0]![0]).toContain(url);
  expect(warn.mock.calls[0]![0]).toContain(`${release}|tag49`);
});

test("a page whose tags exceed Cloud CDN's limits is stored nowhere, so revalidating a dropped tag cannot miss it", () => {
  vi.spyOn(console, "warn").mockImplementation(() => {});

  const response = forCloudCdn(
    new Response("x", {
      headers: { "cache-tag": shapedTags(60), "cache-control": "s-maxage=31536000" },
    }),
    release,
    url,
  );

  expect(response.headers.get("cache-control")).toBe(
    "private, no-cache, no-store, max-age=0, must-revalidate",
  );
  expect(response.headers.has("cache-tag")).toBe(false);
});

test("a page whose tags all fit keeps its cache control", () => {
  const response = forCloudCdn(
    new Response("x", {
      headers: { "cache-tag": shapedTags(3), "cache-control": "s-maxage=60" },
    }),
    release,
    url,
  );

  expect(response.headers.get("cache-control")).toBe("s-maxage=60");
  expect(tagsOf(response)).toBe(`${release},${shapedTags(3)}`);
});

const uncacheable = "private, no-cache, no-store, max-age=0, must-revalidate";

test("a response Cloud CDN would store under s-maxage is withheld from it, because nothing tags it for clearing", () => {
  expect(withholdFromCloudCdn(null)).toBeNull();
  expect(withholdFromCloudCdn("s-maxage=60, stale-while-revalidate=31535940")).toBe(uncacheable);
  expect(withholdFromCloudCdn("S-MaxAge=31536000")).toBe(uncacheable);
  expect(withholdFromCloudCdn("private, no-cache, no-store, max-age=0, must-revalidate")).toBe(
    uncacheable,
  );
  expect(withholdFromCloudCdn("public, max-age=0")).toBe("public, max-age=0");
});

test("an immutable response stays storable by Cloud CDN, because a hashed file never needs clearing", () => {
  expect(withholdFromCloudCdn("public, max-age=31536000, immutable")).toBe(
    "public, max-age=31536000, immutable",
  );
  expect(withholdFromCloudCdn("public, s-maxage=60, immutable")).toBe(
    "public, s-maxage=60, immutable",
  );
});

test("a next start response that names s-maxage leaves the server private and unstorable", async () => {
  class TestResponse extends http.ServerResponse {}
  withholdResponsesFromCloudCdn(TestResponse.prototype);
  const server = http.createServer({ ServerResponse: TestResponse }, (req, res) => {
    if (req.url === "/static")
      res.setHeader("cache-control", "public, max-age=31536000, immutable");
    else res.setHeader("cache-control", "s-maxage=60");
    res.end("x");
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  try {
    const base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
    expect((await fetch(`${base}/page`)).headers.get("cache-control")).toBe(uncacheable);
    expect((await fetch(`${base}/static`)).headers.get("cache-control")).toBe(
      "public, max-age=31536000, immutable",
    );
  } finally {
    await new Promise((resolve) => server.close(resolve));
  }
});

async function cacheControlSeenFor(
  respond: (res: http.ServerResponse) => void,
): Promise<{ cacheControl: string | null; other: string | null }> {
  class TestResponse extends http.ServerResponse {}
  withholdResponsesFromCloudCdn(TestResponse.prototype);
  const server = http.createServer({ ServerResponse: TestResponse }, (_req, res) => {
    respond(res);
    res.end("x");
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  try {
    const response = await fetch(`http://127.0.0.1:${(server.address() as AddressInfo).port}/`);
    return {
      cacheControl: response.headers.get("cache-control"),
      other: response.headers.get("x-other"),
    };
  } finally {
    await new Promise((resolve) => server.close(resolve));
  }
}

test("a Cache-Control passed to writeHead as an object is withheld whatever its key's case", async () => {
  for (const key of ["Cache-Control", "cache-control", "CACHE-CONTROL"]) {
    const seen = await cacheControlSeenFor((res) =>
      res.writeHead(200, { [key]: "s-maxage=60", "x-other": "kept" }),
    );
    expect(seen).toEqual({ cacheControl: uncacheable, other: "kept" });
  }
});

test("a Cache-Control passed to writeHead after a status message is withheld", async () => {
  const seen = await cacheControlSeenFor((res) =>
    res.writeHead(200, "Fine", { "Cache-Control": "s-maxage=60", "x-other": "kept" }),
  );
  expect(seen).toEqual({ cacheControl: uncacheable, other: "kept" });
});

test("a Cache-Control passed to writeHead as a flat raw array is withheld", async () => {
  const seen = await cacheControlSeenFor((res) =>
    res.writeHead(200, ["x-other", "kept", "Cache-Control", "s-maxage=60"]),
  );
  expect(seen).toEqual({ cacheControl: uncacheable, other: "kept" });
  const afterMessage = await cacheControlSeenFor((res) =>
    res.writeHead(200, "Fine", ["Cache-Control", "s-maxage=60", "x-other", "kept"]),
  );
  expect(afterMessage).toEqual({ cacheControl: uncacheable, other: "kept" });
});

test("an immutable or unstorable Cache-Control passed to writeHead is left as it is", async () => {
  const immutable = "public, max-age=31536000, immutable";
  const asObject = await cacheControlSeenFor((res) =>
    res.writeHead(200, { "cache-control": immutable }),
  );
  expect(asObject.cacheControl).toBe(immutable);
  const asArray = await cacheControlSeenFor((res) =>
    res.writeHead(200, ["cache-control", immutable]),
  );
  expect(asArray.cacheControl).toBe(immutable);
  const unstorable = await cacheControlSeenFor((res) =>
    res.writeHead(200, { "cache-control": "public, max-age=0" }),
  );
  expect(unstorable.cacheControl).toBe("public, max-age=0");
});

test("a Cache-Control staged with setHeader is still withheld when writeHead carries other headers", async () => {
  const seen = await cacheControlSeenFor((res) => {
    res.setHeader("cache-control", "s-maxage=60");
    res.writeHead(200, { "x-other": "kept" });
  });
  expect(seen).toEqual({ cacheControl: uncacheable, other: "kept" });
});
