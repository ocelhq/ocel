import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { type CacheEntryFile, variantHeadersFile } from "@framework/next-cache";
import { runWithWaitUntil } from "@framework/node-runtime/background";
import type { TagRecord } from "@platform/edge-contract/purge";
import { afterEach, beforeEach, expect, onTestFinished, test, vi } from "vitest";
import OcelCacheHandler from "../src/cache-handler.mjs";
import type { CacheStore } from "../src/cache-store.mjs";
import { installNextHost } from "../src/host.mjs";
import { collectTags, notedTags } from "../src/origin-tags.mjs";
import { purgedSince, purgeMark } from "../src/purge-signal.mjs";
import { noteServedRoute, noteUncachedRender } from "../src/served-route.mjs";
import { setTagClockStore } from "../src/tag-clock.mjs";
import { writeServerDistFixture } from "../test-support/server-dist-fixture.mjs";

function fakeStore() {
  const entries = new Map<string, CacheEntryFile>();
  const fetches = new Map<string, CacheEntryFile>();
  const tags = new Map<string, TagRecord>();
  let failReads = false;
  let gate: Promise<void> | null = null;
  const calls = { readEntry: 0, writeEntry: 0 };

  const store: CacheStore & {
    entries: typeof entries;
    fetches: typeof fetches;
    tags: typeof tags;
    calls: typeof calls;
    breakReads(): void;
    holdWrites(): () => void;
  } = {
    entries,
    fetches,
    tags,
    calls,
    breakReads() {
      failReads = true;
    },
    holdWrites() {
      let release!: () => void;
      gate = new Promise<void>((resolve) => (release = resolve));
      return () => {
        gate = null;
        release();
      };
    },
    async readEntry(key) {
      calls.readEntry++;
      if (failReads) throw new Error("the store is down");
      return entries.get(key) ?? null;
    },
    async writeEntry(key, entry) {
      calls.writeEntry++;
      if (gate) await gate;
      entries.set(key, entry);
    },
    async readFetch(hash) {
      if (failReads) throw new Error("the store is down");
      return fetches.get(hash) ?? null;
    },
    async writeFetch(hash, entry) {
      if (gate) await gate;
      fetches.set(hash, entry);
    },
  };
  OcelCacheHandler.store = Promise.resolve(store);
  setTagClockStore({
    async readEntry() {
      return null;
    },
    async writeEntry() {},
    async readTagSnapshot() {
      return { status: "unusable" };
    },
    async writeTag(name, record) {
      if (gate) await gate;
      const { writtenAt: _, ...written } = record;
      tags.set(name, { ...tags.get(name), ...written });
      return true;
    },
  });
  return store;
}

beforeEach(() => {
  setTagClockStore(null);
});

afterEach(() => {
  vi.unstubAllEnvs();
  OcelCacheHandler.store = undefined;
  OcelCacheHandler.variantHeaders = undefined;
  installNextHost({});
});

function fakeSnapshot(records: Record<string, TagRecord> = {}) {
  const reads = { count: 0 };
  setTagClockStore({
    async readEntry() {
      return null;
    },
    async writeEntry() {},
    async readTagSnapshot() {
      reads.count++;
      return { status: "fresh", records, cursor: '"v1"' };
    },
    async writeTag() {
      return true;
    },
  });
  return reads;
}

async function invocation(fn: () => Promise<unknown>): Promise<Promise<unknown>[]> {
  const deferred: Promise<unknown>[] = [];
  await runWithWaitUntil((task) => {
    deferred.push(task);
  }, fn);
  return deferred;
}

function seedPage(
  store: ReturnType<typeof fakeStore>,
  key: string,
  opts: {
    tags?: string;
    lastModified?: number;
    cacheControl?: { revalidate: number | false };
  } = {},
) {
  store.entries.set(key, {
    lastModified: opts.lastModified ?? 1_000,
    value: {
      kind: "APP_PAGE",
      html: "<html>hi</html>",
      rscData: Buffer.from("RSC").toString("base64"),
      status: 200,
      headers: opts.tags ? { "x-next-cache-tags": opts.tags } : {},
    },
    ...(opts.cacheControl && { cacheControl: opts.cacheControl }),
  });
}

test("serves a seeded prerender and restores its binary payloads", async () => {
  const store = fakeStore();
  seedPage(store, "index");

  const entry = await new OcelCacheHandler().get("/", { kind: "APP_PAGE" });

  expect(entry?.value.html).toBe("<html>hi</html>");
  expect(Buffer.isBuffer(entry?.value.rscData)).toBe(true);
  expect(entry?.value.rscData.toString()).toBe("RSC");
});

test("answers an RSC request with the RSC variant's headers", async () => {
  const store = fakeStore();
  seedPage(store, "index");
  store.entries.get("index")!.value.headers = {
    "content-type": "text/html; charset=utf-8",
    vary: "RSC",
  };
  store.entries.get("index")!.value.rscHeaders = {
    "content-type": "text/x-component",
    vary: "RSC",
  };

  const handler = new OcelCacheHandler({ _requestHeaders: { rsc: "1" } });
  const entry = await handler.get("/", { kind: "APP_PAGE" });

  expect(entry?.value.headers["content-type"]).toBe("text/x-component");
});

test("negotiates on the runtime's mark once Next has stripped the flight headers", async () => {
  const store = fakeStore();
  seedPage(store, "index");
  store.entries.get("index")!.value.headers = {
    "content-type": "text/html; charset=utf-8",
  };
  store.entries.get("index")!.value.rscHeaders = {
    "content-type": "text/x-component",
  };

  const headers: Record<string | symbol, any> = { rsc: "1" };
  headers[Symbol.for("ocel.rsc-request")] = true;
  delete headers.rsc;

  const entry = await new OcelCacheHandler({ _requestHeaders: headers }).get("/", {
    kind: "APP_PAGE",
  });

  expect(entry?.value.headers["content-type"]).toBe("text/x-component");
});

test("the runtime's mark is invisible to header enumeration", () => {
  const headers: Record<string | symbol, any> = {};
  headers[Symbol.for("ocel.rsc-request")] = true;

  expect(Object.keys(headers)).toEqual([]);
  expect(JSON.stringify(headers)).toBe("{}");
  expect({ ...headers }[Symbol.for("ocel.rsc-request")]).toBe(true);
});

test("leaves a non-RSC request on the html variant's headers", async () => {
  const store = fakeStore();
  seedPage(store, "index");
  store.entries.get("index")!.value.headers = {
    "content-type": "text/html; charset=utf-8",
  };
  store.entries.get("index")!.value.rscHeaders = {
    "content-type": "text/x-component",
  };

  const entry = await new OcelCacheHandler().get("/", { kind: "APP_PAGE" });

  expect(entry?.value.headers["content-type"]).toBe("text/html; charset=utf-8");
});

test("drops the html content-type when an entry predates variant capture", async () => {
  const store = fakeStore();
  seedPage(store, "index", { tags: "products" });
  store.entries.get("index")!.value.headers = {
    "content-type": "text/html; charset=utf-8",
    "x-next-cache-tags": "products",
  };

  const handler = new OcelCacheHandler({ _requestHeaders: { rsc: "1" } });
  const entry = await handler.get("/", { kind: "APP_PAGE" });

  expect(entry?.value.headers).toEqual({ "x-next-cache-tags": "products" });
});

test("hands the request the tags of the entry it served", async () => {
  const store = fakeStore();
  seedPage(store, "index", { tags: "products" });
  const requestHeaders: any = {};
  collectTags(requestHeaders);

  await new OcelCacheHandler({ _requestHeaders: requestHeaders }).get("/", {
    kind: "APP_PAGE",
  });

  expect(notedTags(requestHeaders)).toEqual(["products"]);
});

test("hands the request the tags of a render it stored, not just of a hit", async () => {
  fakeStore();
  const requestHeaders: any = {};
  collectTags(requestHeaders);

  await new OcelCacheHandler({ _requestHeaders: requestHeaders }).set(
    "/",
    {
      kind: "APP_PAGE",
      html: "<html>hi</html>",
      status: 200,
      headers: { "x-next-cache-tags": "products,_N_T_/products" },
    },
    { cacheControl: { revalidate: 60 } },
  );

  expect(notedTags(requestHeaders)).toEqual(["products", "_N_T_/products"]);
});

test("unions the tags of every entry a request composed", async () => {
  const store = fakeStore();
  seedPage(store, "index", { tags: "products,_N_T_/layout" });
  seedPage(store, "cart", { tags: "cart,products" });
  const requestHeaders: any = {};
  collectTags(requestHeaders);
  const handler = new OcelCacheHandler({ _requestHeaders: requestHeaders });

  await handler.get("/", { kind: "APP_PAGE" });
  await handler.get("/cart", { kind: "APP_PAGE" });

  expect(notedTags(requestHeaders)).toEqual(["products", "_N_T_/layout", "cart"]);
});

test("notes a tag in Next's spelling, leaving each edge to apply its own tag limits", async () => {
  const store = fakeStore();
  const many = Array.from({ length: 1_200 }, (_, i) => `t${i}`);
  seedPage(store, "index", { tags: ["two words", ...many].join(",") });
  const requestHeaders: any = {};
  collectTags(requestHeaders);

  await new OcelCacheHandler({ _requestHeaders: requestHeaders }).get("/", {
    kind: "APP_PAGE",
  });

  expect(notedTags(requestHeaders)).toEqual(["two words", ...many]);
});

test("notes nothing on a request the entrypoint never installed a collector on", async () => {
  const store = fakeStore();
  seedPage(store, "index", { tags: "products" });
  const requestHeaders: any = {};

  await new OcelCacheHandler({ _requestHeaders: requestHeaders }).get("/", {
    kind: "APP_PAGE",
  });

  expect(notedTags(requestHeaders)).toEqual([]);
});

test("misses when no entry was seeded", async () => {
  fakeStore();
  const entry = await new OcelCacheHandler().get("/absent", { kind: "APP_PAGE" });
  expect(entry).toBeNull();
});

test("expires an entry whose tag was revalidated after it was written", async () => {
  const store = fakeStore();
  seedPage(store, "index", { tags: "_N_T_/layout,products", lastModified: 1_000 });

  await new OcelCacheHandler().revalidateTag("products");
  const entry = await new OcelCacheHandler().get("/", { kind: "APP_PAGE" });

  expect(entry).toBeNull();
});

function servedBy(route: { revalidate: number }) {
  const requestHeaders: any = {};
  noteServedRoute(requestHeaders, route);
  return { requestHeaders, handler: new OcelCacheHandler({ _requestHeaders: requestHeaders }) };
}

test("announces an expired tag as a purge so the request can send it in-band", async () => {
  fakeStore();
  const mark = purgeMark();

  await new OcelCacheHandler().revalidateTag("products");

  expect(purgedSince(mark)).toMatchObject([{ tag: "products", mode: "purge" }]);
});

test("announces a tag revalidated with a profile as an invalidate", async () => {
  fakeStore();
  const mark = purgeMark();

  await new OcelCacheHandler().revalidateTag("products", { expire: 3600 });

  expect(purgedSince(mark)).toMatchObject([{ tag: "products", mode: "invalidate" }]);
});

test("announces before the tag write finishes, so a response sent meanwhile carries it", async () => {
  const store = fakeStore();
  const release = store.holdWrites();
  const mark = purgeMark();

  const pending = new OcelCacheHandler().revalidateTag("products");
  await Promise.resolve();

  expect(purgedSince(mark)).toHaveLength(1);
  release();
  await pending;
});

test("announces nothing for a revalidation naming no tags", async () => {
  fakeStore();
  const mark = purgeMark();

  await new OcelCacheHandler().revalidateTag([]);

  expect(purgedSince(mark)).toEqual([]);
});

test("keeps an entry written after its tag was revalidated", async () => {
  const store = fakeStore();
  fakeSnapshot({ products: { expired: 500 } });
  seedPage(store, "index", { tags: "products", lastModified: 1_000 });

  const entry = await new OcelCacheHandler().get("/", { kind: "APP_PAGE" });

  expect(entry).not.toBeNull();
});

test("leaves entries usable until a future expiry actually arrives", async () => {
  const store = fakeStore();
  seedPage(store, "index", { tags: "products", lastModified: 1_000 });

  await new OcelCacheHandler().revalidateTag("products", { expire: 3600 });

  const record = store.tags.get("products");
  expect(record?.stale).toBeGreaterThan(0);
  expect(record?.expired).toBeGreaterThan(Date.now());
  expect(await new OcelCacheHandler().get("/", { kind: "APP_PAGE" })).not.toBeNull();
});

test("untagged entries never read the tag snapshot", async () => {
  const store = fakeStore();
  const reads = fakeSnapshot();
  seedPage(store, "index");

  await new OcelCacheHandler().get("/", { kind: "APP_PAGE" });

  expect(reads.count).toBe(0);
});

test("reports a miss rather than throwing when the store fails", async () => {
  const store = fakeStore();
  seedPage(store, "index");
  store.breakReads();

  await expect(new OcelCacheHandler().get("/", { kind: "APP_PAGE" })).resolves.toBeNull();
});

test("round-trips a page written by set through get", async () => {
  fakeStore();
  const handler = new OcelCacheHandler();

  const deferred = await invocation(() =>
    handler.set(
      "/blog",
      {
        kind: "APP_PAGE",
        html: { toUnchunkedString: () => "<html>blog</html>" },
        rscData: Buffer.from("BLOG-RSC"),
        status: 200,
        headers: { "x-next-cache-tags": "posts" },
        segmentData: new Map([["/_tree", Buffer.from("TREE")]]),
      },
      {},
    ),
  );
  await Promise.all(deferred);

  const entry = await handler.get("/blog", { kind: "APP_PAGE" });
  expect(entry?.value.html).toBe("<html>blog</html>");
  expect(entry?.value.rscData.toString()).toBe("BLOG-RSC");
  expect(entry?.value.segmentData.get("/_tree").toString()).toBe("TREE");
});

test("records the render's cache control on the entry", async () => {
  const store = fakeStore();
  const handler = new OcelCacheHandler();

  const deferred = await invocation(() =>
    handler.set(
      "/blog",
      {
        kind: "APP_ROUTE",
        body: Buffer.from('{"ok":true}'),
        status: 200,
        headers: {},
      },
      { cacheControl: { revalidate: 1, expire: 31536000 } },
    ),
  );
  await Promise.all(deferred);

  expect(store.entries.get("blog")?.cacheControl).toEqual({
    revalidate: 1,
    expire: 31536000,
  });
});

test("leaves a fetch entry's window unrecorded", async () => {
  const store = fakeStore();
  const handler = new OcelCacheHandler();

  const deferred = await invocation(() =>
    handler.set(
      "hash",
      { kind: "FETCH", data: { body: "x" }, revalidate: 60 },
      { fetchCache: true, tags: ["posts"] },
    ),
  );
  await Promise.all(deferred);

  expect(store.fetches.get("hash")?.cacheControl).toBeUndefined();
});

function seedProjection(projection: Record<string, Record<string, unknown>>): void {
  OcelCacheHandler.variantHeaders = projection;
}

function bundleProjection(contents: string | null): void {
  bundleRoot ??= mkdtempSync(join(tmpdir(), "ocel-bundle-"));
  installNextHost({ functionDir: bundleRoot });
  const path = join(bundleRoot, variantHeadersFile);
  rmSync(path, { force: true });
  if (contents !== null) writeFileSync(path, contents);
}

let bundleRoot: string | undefined;

async function revalidate(key: string, extra: Record<string, unknown> = {}) {
  const deferred = await invocation(() =>
    new OcelCacheHandler().set(
      key,
      {
        kind: "APP_PAGE",
        html: { toUnchunkedString: () => "<html>fresh</html>" },
        status: 200,
        headers: { "content-type": "text/html; charset=utf-8" },
        ...extra,
      },
      {},
    ),
  );
  await Promise.all(deferred);
}

test("reseeds the build's variant headers on a revalidation write", async () => {
  const store = fakeStore();
  seedProjection({
    blog: {
      rscHeaders: { "content-type": "text/x-component" },
      segmentHeaders: {
        "content-type": "text/x-component",
        "x-nextjs-postponed": "2",
      },
    },
  });

  await revalidate("/blog", {
    segmentData: new Map([["/_tree", Buffer.from("TREE")]]),
  });

  const rewritten = store.entries.get("blog");
  expect(rewritten?.value.html).toBe("<html>fresh</html>");
  expect(rewritten?.value.rscHeaders).toEqual({ "content-type": "text/x-component" });
  expect(rewritten?.value.segmentHeaders).toEqual({
    "content-type": "text/x-component",
    "x-nextjs-postponed": "2",
  });
});

test("a revalidation write is one write and no read", async () => {
  const store = fakeStore();
  seedProjection({ blog: { rscHeaders: { "content-type": "text/x-component" } } });

  await revalidate("/blog");

  expect(store.calls).toEqual({ readEntry: 0, writeEntry: 1 });
});

test("writes a route the projection does not name", async () => {
  const store = fakeStore();
  seedProjection({});

  await revalidate("/blog");

  const written = store.entries.get("blog");
  expect(written?.value.html).toBe("<html>fresh</html>");
  expect(written?.value.rscHeaders).toBeUndefined();
  expect(written?.value.segmentHeaders).toBeUndefined();
});

test("a rewritten entry still serves segment prefetch and RSC negotiation", async () => {
  const store = fakeStore();
  seedProjection({
    blog: {
      rscHeaders: { "content-type": "text/x-component" },
      segmentHeaders: {
        "content-type": "text/x-component",
        "x-nextjs-postponed": "2",
      },
    },
  });

  await revalidate("/blog", {
    segmentData: new Map([["/_tree", Buffer.from("TREE")]]),
  });

  const written = store.entries.get("blog")!;
  expect(written.value.segmentHeaders["x-nextjs-postponed"]).toBe("2");
  expect(written.value.segmentData["/_tree"]).toBe(Buffer.from("TREE").toString("base64"));

  const served = await new OcelCacheHandler({
    _requestHeaders: { rsc: "1" },
  }).get("/blog", { kind: "APP_PAGE" });
  expect(served?.value.headers["content-type"]).toBe("text/x-component");
});

const bundles: [name: string, contents: string | null][] = [
  ["no file at all", null],
  ["an empty file", ""],
  ["malformed JSON", "{"],
  ["JSON null", "null"],
  ["an array", "[]"],
  ["a bare string", '"blog"'],
  ["a number", "7"],
];

for (const [name, contents] of bundles) {
  test(`writes the entry when the bundle contains ${name}`, async () => {
    const store = fakeStore();
    bundleProjection(contents);

    await revalidate("/blog");

    const written = store.entries.get("blog");
    expect(written?.value.html).toBe("<html>fresh</html>");
    expect(written?.value.rscHeaders).toBeUndefined();
    expect(written?.value.segmentHeaders).toBeUndefined();
  });
}

test("reads the build's variant headers from the function directory its host declares", async () => {
  const store = fakeStore();
  bundleProjection(
    JSON.stringify({ blog: { rscHeaders: { "content-type": "text/x-component" } } }),
  );

  await revalidate("/blog");

  expect(store.entries.get("blog")?.value.rscHeaders).toEqual({
    "content-type": "text/x-component",
  });
});

test("reads an unreadable projection once", async () => {
  const store = fakeStore();
  bundleProjection("null");

  await revalidate("/blog");
  bundleProjection(
    JSON.stringify({ blog: { rscHeaders: { "content-type": "text/x-component" } } }),
  );
  await revalidate("/blog");

  expect(store.entries.get("blog")?.value.rscHeaders).toBeUndefined();
});

test("the rendering request does not wait for the entry write", async () => {
  const store = fakeStore();
  const release = store.holdWrites();
  const handler = new OcelCacheHandler();

  const deferred = await invocation(() =>
    handler.set("/blog", { kind: "PAGES", html: "<html>blog</html>", pageData: {} }, {}),
  );
  expect(store.entries.size).toBe(0);

  release();
  await Promise.all(deferred);
  expect(store.entries.get("blog")).toBeDefined();
});

test("serializes the render result before deferring the write", async () => {
  const store = fakeStore();
  const release = store.holdWrites();
  const handler = new OcelCacheHandler();
  let renders = 0;

  const deferred = await invocation(() =>
    handler.set(
      "/blog",
      {
        kind: "APP_PAGE",
        html: {
          toUnchunkedString: () => {
            renders++;
            return "<html>blog</html>";
          },
        },
        status: 200,
        headers: {},
      },
      {},
    ),
  );
  expect(renders).toBe(1);

  release();
  await Promise.all(deferred);
  expect(store.entries.get("blog")!.value.html).toBe("<html>blog</html>");
});

test("takes fetch tags from the request context", async () => {
  const store = fakeStore();
  store.fetches.set("abc", {
    lastModified: 1_000,
    value: { kind: "FETCH", data: {}, tags: [] },
  });

  await new OcelCacheHandler().revalidateTag("api");
  const entry = await new OcelCacheHandler().get("abc", {
    kind: "FETCH",
    tags: ["api"],
  });

  expect(entry).toBeNull();
});

test("reads back a fetch entry whose tags the request also names", async () => {
  const store = fakeStore();
  const handler = new OcelCacheHandler();

  const deferred = await invocation(() =>
    handler.set(
      "abc",
      { kind: "FETCH", data: { body: "cached" }, revalidate: 900 },
      { fetchCache: true, tags: ["products"] },
    ),
  );
  await Promise.all(deferred);
  expect(store.fetches.get("abc")!.value.tags).toEqual(["products"]);

  const entry = await handler.get("abc", {
    kind: "FETCH",
    tags: ["products"],
    softTags: ["_N_T_/shop"],
  });

  expect(entry?.value.data.body).toBe("cached");
});

test("keeps fetch entries out of the route-entry store", async () => {
  const store = fakeStore();
  const handler = new OcelCacheHandler();

  const deferred = await invocation(() =>
    handler.set(
      "deadbeef",
      { kind: "FETCH", data: { body: "upstream" }, revalidate: 900 },
      { fetchCache: true, tags: [] },
    ),
  );
  await Promise.all(deferred);

  expect(store.fetches.get("deadbeef")!.value.data.body).toBe("upstream");
  expect(store.entries.size).toBe(0);

  const back = await handler.get("deadbeef", { kind: "FETCH", tags: [] });
  expect(back!.value.data.body).toBe("upstream");
});

test("marking a tag stale preserves an expiry set earlier", async () => {
  const store = fakeStore();
  seedPage(store, "index", { tags: "products", lastModified: 1_000 });
  const handler = new OcelCacheHandler();

  await handler.revalidateTag("products");
  const expiredAfterFirst = store.tags.get("products")?.expired;

  await handler.revalidateTag("products", {});

  expect(store.tags.get("products")?.expired).toBe(expiredAfterFirst);
  expect(store.tags.get("products")?.stale).toBeGreaterThan(0);
  expect(await handler.get("/", { kind: "APP_PAGE" })).toBeNull();
});

test("holds the request's last byte for the tag write a revalidation makes", async () => {
  const store = fakeStore();
  const release = store.holdWrites();
  let revalidated: Promise<void> | undefined;

  const held = await invocation(async () => {
    revalidated = new OcelCacheHandler().revalidateTag("products");
  });
  release();
  await revalidated;

  expect(held).toHaveLength(1);
  expect(store.tags.get("products")?.expired).toBeGreaterThan(0);
});

function seedFetch(store: ReturnType<typeof fakeStore>, ageMs: number, tags: string[] = []) {
  store.fetches.set("abc", {
    lastModified: Date.now() - ageMs,
    value: { kind: "FETCH", data: { body: "cached" }, revalidate: 60, tags },
  });
}

test("a stale fetch entry is served while it refreshes", async () => {
  const store = fakeStore();
  seedFetch(store, 120_000);

  const entry = await new OcelCacheHandler().get("abc", {
    kind: "FETCH",
    tags: [],
    revalidate: 60,
  });

  expect(entry?.value.data.body).toBe("cached");
});

function behindCachingEdge() {
  vi.stubEnv("OCEL_EDGE_CACHE", "all");
}

const hour = 3_600_000;

test("behind a caching edge, a time-stale page entry is a miss so Next renders it", async () => {
  behindCachingEdge();
  const store = fakeStore();
  seedPage(store, "index", {
    lastModified: Date.now() - 120_000,
    cacheControl: { revalidate: 60 },
  });

  expect(await new OcelCacheHandler().get("/", { kind: "APP_PAGE" })).toBeNull();
});

test("behind a caching edge, a fresh page entry is served without a render", async () => {
  behindCachingEdge();
  const store = fakeStore();
  seedPage(store, "index", {
    lastModified: Date.now() - 1_000,
    cacheControl: { revalidate: 60 },
  });

  const entry = await new OcelCacheHandler().get("/", { kind: "APP_PAGE" });

  expect(entry?.value.html).toBe("<html>hi</html>");
});

test("behind a caching edge, a page entry that never revalidates is served", async () => {
  behindCachingEdge();
  const store = fakeStore();
  seedPage(store, "index", {
    lastModified: Date.now() - hour,
    cacheControl: { revalidate: false },
  });

  expect(await new OcelCacheHandler().get("/", { kind: "APP_PAGE" })).not.toBeNull();
});

test("behind a caching edge, a time-stale page entry of a served route is a miss", async () => {
  behindCachingEdge();
  const store = fakeStore();
  seedPage(store, "index", { lastModified: Date.now() - 120_000 });
  const { handler } = servedBy({ revalidate: 60 });

  expect(await handler.get("/", { kind: "APP_PAGE" })).toBeNull();
});

test("behind a caching edge, a tag-stale entry is a miss", async () => {
  behindCachingEdge();
  const store = fakeStore();
  seedPage(store, "index", { tags: "products", lastModified: Date.now() - 10_000 });
  fakeSnapshot({ products: { stale: Date.now() - 5_000 } });

  expect(await new OcelCacheHandler().get("/", { kind: "APP_PAGE" })).toBeNull();
});

test("behind a caching edge, a time-stale fetch entry is a miss", async () => {
  behindCachingEdge();
  const store = fakeStore();
  seedFetch(store, 120_000);

  const entry = await new OcelCacheHandler().get("abc", {
    kind: "FETCH",
    tags: [],
    revalidate: 60,
  });

  expect(entry).toBeNull();
});

test("behind a caching edge, a fresh fetch entry is served", async () => {
  behindCachingEdge();
  const store = fakeStore();
  seedFetch(store, 1_000);

  const entry = await new OcelCacheHandler().get("abc", {
    kind: "FETCH",
    tags: [],
    revalidate: 60,
  });

  expect(entry?.value.data.body).toBe("cached");
});

test("behind a caching edge, a render resolves only once its page entry is written", async () => {
  behindCachingEdge();
  const store = fakeStore();
  const release = store.holdWrites();
  let resolved = false;

  const written = new OcelCacheHandler()
    .set("/blog", { kind: "PAGES", html: "<html>blog</html>", pageData: {} }, {})
    .then(() => {
      resolved = true;
    });
  await new Promise((resolve) => setTimeout(resolve, 10));

  expect(resolved).toBe(false);
  release();
  await written;
  expect(store.entries.get("blog")?.value.html).toBe("<html>blog</html>");
});

test("behind a caching edge, a render resolves only once its fetch entry is written", async () => {
  behindCachingEdge();
  const store = fakeStore();
  const release = store.holdWrites();
  let resolved = false;

  const written = new OcelCacheHandler()
    .set("abc", { kind: "FETCH", data: { body: "x" }, revalidate: 60 }, { tags: [] })
    .then(() => {
      resolved = true;
    });
  await new Promise((resolve) => setTimeout(resolve, 10));

  expect(resolved).toBe(false);
  release();
  await written;
  expect(store.fetches.get("abc")?.value.data.body).toBe("x");
});

test("without a caching edge, a time-stale page entry is still served", async () => {
  const store = fakeStore();
  seedPage(store, "index", {
    lastModified: Date.now() - 120_000,
    cacheControl: { revalidate: 60 },
  });

  expect(await new OcelCacheHandler().get("/", { kind: "APP_PAGE" })).not.toBeNull();
});

test("a stale entry behind a caching edge is rendered once and then served from the store", async () => {
  behindCachingEdge();
  const store = fakeStore();
  seedPage(store, "index", {
    lastModified: Date.now() - 120_000,
    cacheControl: { revalidate: 60 },
  });
  const handler = new OcelCacheHandler();

  expect(await handler.get("/", { kind: "APP_PAGE" })).toBeNull();
  await handler.set(
    "/",
    { kind: "PAGES", html: "<html>fresh</html>", pageData: {} },
    { cacheControl: { revalidate: 60 } },
  );
  const entry = await new OcelCacheHandler().get("/", { kind: "APP_PAGE" });

  expect(entry?.value.html).toBe("<html>fresh</html>");
});

test("behind a caching edge, a fetch inside a served page is judged by its own window", async () => {
  behindCachingEdge();
  const store = fakeStore();
  seedFetch(store, 11_000);
  const { handler } = servedBy({ revalidate: 3600 });

  const entry = await handler.get("abc", { kind: "FETCH", tags: [], revalidate: 10 });

  expect(entry).toBeNull();
});

test("behind a caching edge, a fetch with no window of its own is judged by the window it was stored with", async () => {
  behindCachingEdge();
  const store = fakeStore();
  seedFetch(store, 120_000);
  const { handler } = servedBy({ revalidate: 3600 });

  const entry = await handler.get("abc", { kind: "FETCH", tags: [], revalidate: 0 });

  expect(entry).toBeNull();
});

function uncachedRender() {
  const requestHeaders: any = {};
  noteUncachedRender(requestHeaders);
  return new OcelCacheHandler({ _requestHeaders: requestHeaders });
}

test("behind a caching edge, a time-stale fetch entry is served to a render no edge stores", async () => {
  behindCachingEdge();
  const store = fakeStore();
  seedFetch(store, 120_000);

  const entry = await uncachedRender().get("abc", { kind: "FETCH", tags: [], revalidate: 60 });

  expect(entry?.value.data.body).toBe("cached");
});

test("behind a caching edge, a tag-stale fetch entry is a miss even to a render no edge stores", async () => {
  behindCachingEdge();
  const store = fakeStore();
  seedFetch(store, 10_000, ["products"]);
  fakeSnapshot({ products: { stale: Date.now() - 5_000 } });

  const entry = await uncachedRender().get("abc", {
    kind: "FETCH",
    tags: ["products"],
    revalidate: 60,
  });

  expect(entry).toBeNull();
});

test("behind a caching edge, a time-stale page entry is a miss even to a render no edge stores", async () => {
  behindCachingEdge();
  const store = fakeStore();
  seedPage(store, "index", {
    lastModified: Date.now() - 120_000,
    cacheControl: { revalidate: 60 },
  });

  expect(await uncachedRender().get("/", { kind: "APP_PAGE" })).toBeNull();
});

test("behind a caching edge, a render no edge stores does not wait for its fetch entry to be written", async () => {
  behindCachingEdge();
  const store = fakeStore();
  const release = store.holdWrites();
  const requestHeaders: any = {};
  noteUncachedRender(requestHeaders);
  let resolved = false;

  const deferred = await invocation(() =>
    new OcelCacheHandler({ _requestHeaders: requestHeaders })
      .set("abc", { kind: "FETCH", data: { body: "x" }, revalidate: 60 }, { tags: [] })
      .then(() => {
        resolved = true;
      }),
  );

  expect(resolved).toBe(true);
  release();
  await Promise.all(deferred);
  expect(store.fetches.get("abc")?.value.data.body).toBe("x");
});

const isrTags = "_N_T_/layout,_N_T_/cache/isr/page,_N_T_/cache/isr,products";

function prerenderedIsrPage(writtenAt?: number) {
  const dist = writeServerDistFixture(
    {
      routes: {
        "/cache/isr": {
          initialRevalidateSeconds: 15,
          initialExpireSeconds: 31_536_000,
          srcRoute: "/cache/isr",
          dataRoute: "/cache/isr.rsc",
        },
      },
    },
    {
      "app/cache/isr.html": "<html>prerendered</html>",
      "app/cache/isr.rsc": "RSC",
      "app/cache/isr.meta": JSON.stringify({
        headers: { "x-nextjs-prerender": "1", "x-next-cache-tags": isrTags },
        segmentPaths: ["/_tree"],
      }),
      "app/cache/isr.segments/_tree.segment.rsc": "TREE",
      "app/cache/dynamic.html": "<html>never prerendered</html>",
    },
    writtenAt,
  );
  onTestFinished(dist.remove);
  return new OcelCacheHandler({ serverDistDir: dist.serverDistDir });
}

test("a store miss for a page next build prerendered serves the files it wrote", async () => {
  fakeStore();
  const writtenAt = Date.now() - 10_000;
  const handler = prerenderedIsrPage(writtenAt);

  const entry = await handler.get("/cache/isr", { kind: "APP_PAGE" });

  expect(entry?.lastModified).toBe(writtenAt);
  expect(entry?.value.kind).toBe("APP_PAGE");
  expect(entry?.value.html).toBe("<html>prerendered</html>");
  expect(entry?.value.headers["x-next-cache-tags"]).toBe(isrTags);
  expect(Buffer.isBuffer(entry?.value.rscData)).toBe(true);
  expect(entry?.value.rscData.toString()).toBe("RSC");
  expect(entry?.value.segmentData.get("/_tree").toString()).toBe("TREE");
});

test("a page next build prerendered is a miss once a tag it carries is revalidated", async () => {
  fakeStore();
  const handler = prerenderedIsrPage();

  await handler.revalidateTag("products");

  expect(await handler.get("/cache/isr", { kind: "APP_PAGE" })).toBeNull();
});

test("a page next build prerendered is a miss once its path is revalidated", async () => {
  fakeStore();
  const handler = prerenderedIsrPage();

  await handler.revalidateTag("_N_T_/cache/isr");

  expect(await handler.get("/cache/isr", { kind: "APP_PAGE" })).toBeNull();
});

test("a store miss for a route next build did not prerender stays a miss", async () => {
  fakeStore();
  const handler = prerenderedIsrPage();

  expect(await handler.get("/cache/dynamic", { kind: "APP_PAGE" })).toBeNull();
});

test("the store's entry wins over the page next build prerendered", async () => {
  const store = fakeStore();
  seedPage(store, "cache/isr", { lastModified: Date.now() });
  const handler = prerenderedIsrPage();

  const entry = await handler.get("/cache/isr", { kind: "APP_PAGE" });

  expect(entry?.value.html).toBe("<html>hi</html>");
});

test("behind a caching edge, a page next build prerendered is a miss once its revalidate window passed", async () => {
  behindCachingEdge();
  fakeStore();
  const handler = prerenderedIsrPage(Date.now() - 60_000);

  expect(await handler.get("/cache/isr", { kind: "APP_PAGE" })).toBeNull();
});

test("behind a caching edge, a page next build prerendered inside its revalidate window is served", async () => {
  behindCachingEdge();
  fakeStore();
  const handler = prerenderedIsrPage(Date.now() - 5_000);

  expect(await handler.get("/cache/isr", { kind: "APP_PAGE" })).not.toBeNull();
});
