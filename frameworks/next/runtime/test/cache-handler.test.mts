import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  type CacheEntryFile,
  refreshHeader,
  type TagRecord,
  variantHeadersFile,
} from "@framework/next-cache";
import { runWithWaitUntil } from "@framework/node-runtime/background";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import OcelCacheHandler from "../src/cache-handler.mjs";
import type { CacheStore } from "../src/cache-store.mjs";
import { installNextHost } from "../src/host.mjs";
import { collectTags, notedTags } from "../src/origin-tags.mjs";
import { readServedEntry } from "../src/refresh.mjs";
import { revalidationTicks } from "../src/revalidation-signal.mjs";
import { setTagClockStore } from "../src/tag-clock.mjs";

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
    async writeTags(names, record) {
      if (gate) await gate;
      for (const n of names) tags.set(n, { ...tags.get(n), ...record });
    },
  };
  OcelCacheHandler.store = Promise.resolve(store);
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
      return { status: "fresh", records, etag: '"v1"' };
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
  opts: { tags?: string; lastModified?: number } = {},
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

test("misses on a refresh that already has this generation, so Next regenerates", async () => {
  const store = fakeStore();
  seedPage(store, "index", { lastModified: 1_000 });
  const handler = new OcelCacheHandler({ _requestHeaders: { [refreshHeader]: "1000" } });

  expect(await handler.get("/", { kind: "APP_PAGE" })).toBeNull();
});

test("serves a refresh the generation newer than the one it has", async () => {
  const store = fakeStore();
  seedPage(store, "index", { lastModified: 2_000 });
  const handler = new OcelCacheHandler({ _requestHeaders: { [refreshHeader]: "1000" } });

  const entry = await handler.get("/", { kind: "APP_PAGE" });

  expect(entry?.lastModified).toBe(2_000);
});

test("notes the generation of the page it served, so a stale hit can be refreshed", async () => {
  const store = fakeStore();
  seedPage(store, "index", { lastModified: 1_000 });
  const requestHeaders: any = {};

  await new OcelCacheHandler({ _requestHeaders: requestHeaders }).get("/", { kind: "APP_PAGE" });

  expect(readServedEntry(requestHeaders)).toBe(1_000);
});

test("a refresh's render resolves only once its entry is written", async () => {
  const store = fakeStore();
  const release = store.holdWrites();
  const handler = new OcelCacheHandler({ _requestHeaders: { [refreshHeader]: "1000" } });
  let resolved = false;

  const written = handler
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

test("a refresh leaves fetch entries to their own windows", async () => {
  const store = fakeStore();
  store.fetches.set("abc", {
    lastModified: 500,
    value: { kind: "FETCH", data: { body: "cached" }, revalidate: 900, tags: [] },
  });
  const handler = new OcelCacheHandler({ _requestHeaders: { [refreshHeader]: "1000" } });

  const entry = await handler.get("abc", { kind: "FETCH", tags: [] });

  expect(entry?.value.data.body).toBe("cached");
});

test("ticks the revalidation signal so the request can announce it", async () => {
  fakeStore();
  const before = revalidationTicks();

  await new OcelCacheHandler().revalidateTag("products");

  expect(revalidationTicks()).toBe(before + 1);
});

test("ticks before the tag write finishes, so a response sent meanwhile announces it", async () => {
  const store = fakeStore();
  const release = store.holdWrites();
  const before = revalidationTicks();

  const pending = new OcelCacheHandler().revalidateTag("products");
  await Promise.resolve();

  expect(revalidationTicks()).toBe(before + 1);
  release();
  await pending;
});

test("ticks nothing for a revalidation naming no tags", async () => {
  fakeStore();
  const before = revalidationTicks();

  await new OcelCacheHandler().revalidateTag([]);

  expect(revalidationTicks()).toBe(before);
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

test("a stale fetch entry is a miss where CPU stops at the response's end", async () => {
  vi.stubEnv("OCEL_FINISH_BEFORE_RESPONSE_MS", "5000");
  const store = fakeStore();
  seedFetch(store, 120_000);

  const entry = await new OcelCacheHandler().get("abc", {
    kind: "FETCH",
    tags: [],
    revalidate: 60,
  });

  expect(entry).toBeNull();
});

test("a stale fetch entry is served where CPU runs past the response's end", async () => {
  const store = fakeStore();
  seedFetch(store, 120_000);

  const entry = await new OcelCacheHandler().get("abc", {
    kind: "FETCH",
    tags: [],
    revalidate: 60,
  });

  expect(entry?.value.data.body).toBe("cached");
});

test("a fetch entry within its window is served where CPU stops at the response's end", async () => {
  vi.stubEnv("OCEL_FINISH_BEFORE_RESPONSE_MS", "5000");
  const store = fakeStore();
  seedFetch(store, 10_000);

  const entry = await new OcelCacheHandler().get("abc", {
    kind: "FETCH",
    tags: [],
    revalidate: 60,
  });

  expect(entry?.value.data.body).toBe("cached");
});

test("a fetch entry that never goes stale by time is served where CPU stops at the response's end", async () => {
  vi.stubEnv("OCEL_FINISH_BEFORE_RESPONSE_MS", "5000");
  const store = fakeStore();
  seedFetch(store, 120_000);
  delete store.fetches.get("abc")!.value.revalidate;

  const entry = await new OcelCacheHandler().get("abc", {
    kind: "FETCH",
    tags: [],
    revalidate: false,
  });

  expect(entry?.value.data.body).toBe("cached");
});

test("a fetch entry stale by tag is a miss where CPU stops at the response's end", async () => {
  vi.stubEnv("OCEL_FINISH_BEFORE_RESPONSE_MS", "5000");
  const store = fakeStore();
  fakeSnapshot({ api: { stale: Date.now() } });
  seedFetch(store, 10_000, ["api"]);

  const entry = await new OcelCacheHandler().get("abc", {
    kind: "FETCH",
    tags: ["api"],
    revalidate: 60,
  });

  expect(entry).toBeNull();
});
