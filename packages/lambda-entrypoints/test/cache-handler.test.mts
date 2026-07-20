import { afterEach, expect, test } from "vitest";
import OcelCacheHandler from "../src/next/cache-handler.mjs";
import { runWithWaitUntil } from "../src/shared/background.mjs";
import type {
  CacheEntryFile,
  CacheStore,
  TagRecord,
} from "../src/next/cache-store.mjs";

// A stand-in for S3 + DynamoDB that keeps the handler's two backing stores
// separate, so a test can revalidate a tag without touching entries and vice
// versa — exactly the split the real backends have.
function fakeStore() {
  const entries = new Map<string, CacheEntryFile>();
  // Fetch entries live in a different bucket than route entries, so the fake
  // keeps them apart too: a test that reads one must never see the other.
  const fetches = new Map<string, CacheEntryFile>();
  const tags = new Map<string, TagRecord>();
  let failReads = false;
  let gate: Promise<void> | null = null;

  const store: CacheStore & {
    entries: typeof entries;
    fetches: typeof fetches;
    tags: typeof tags;
    breakReads(): void;
    holdWrites(): () => void;
  } = {
    entries,
    fetches,
    tags,
    breakReads() {
      failReads = true;
    },
    // Stalls every entry write until the returned release is called, so a test
    // can prove the request resolved without it rather than racing it.
    holdWrites() {
      let release!: () => void;
      gate = new Promise<void>((resolve) => (release = resolve));
      return () => {
        gate = null;
        release();
      };
    },
    async readEntry(key) {
      if (failReads) throw new Error("s3 is down");
      return entries.get(key) ?? null;
    },
    async writeEntry(key, entry) {
      if (gate) await gate;
      entries.set(key, entry);
    },
    async readFetch(hash) {
      if (failReads) throw new Error("s3 is down");
      return fetches.get(hash) ?? null;
    },
    async writeFetch(hash, entry) {
      if (gate) await gate;
      fetches.set(hash, entry);
    },
    async readTags(names) {
      const found = new Map<string, TagRecord>();
      for (const n of names) {
        const rec = tags.get(n);
        if (rec) found.set(n, rec);
      }
      return found;
    },
    async writeTags(names, record) {
      // Models UpdateItem SET: fields present are overwritten, absent ones kept.
      for (const n of names) tags.set(n, { ...tags.get(n), ...record });
    },
  };
  OcelCacheHandler.store = store;
  return store;
}

afterEach(() => {
  OcelCacheHandler.store = undefined;
});

// Runs `fn` the way the membrane runs an invocation: work it defers is collected
// rather than awaited, so a test can assert what the request itself paid for and
// then settle the rest deliberately.
async function invocation(fn: () => Promise<unknown>): Promise<Promise<unknown>[]> {
  const deferred: Promise<unknown>[] = [];
  await runWithWaitUntil((task) => {
    deferred.push(task);
  }, fn);
  return deferred;
}

// The key the adapter seeds a route's entry under at build time; a mismatch here
// means a deployed route silently re-renders forever.
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
  // Next expects a Buffer back, not the base64 the entry is stored as.
  expect(Buffer.isBuffer(entry?.value.rscData)).toBe(true);
  expect(entry?.value.rscData.toString()).toBe("RSC");
});

test("misses when no entry was seeded", async () => {
  fakeStore();
  const entry = await new OcelCacheHandler().get("/absent", { kind: "APP_PAGE" });
  expect(entry).toBeNull();
});

// The tags for a page kind reach the handler only through the stored entry — the
// get context carries none — so this proves we read them off the right place.
test("expires an entry whose tag was revalidated after it was written", async () => {
  const store = fakeStore();
  seedPage(store, "index", { tags: "_N_T_/layout,products", lastModified: 1_000 });

  await new OcelCacheHandler().revalidateTag("products");
  const entry = await new OcelCacheHandler().get("/", { kind: "APP_PAGE" });

  expect(entry).toBeNull();
});

test("keeps an entry written after its tag was revalidated", async () => {
  const store = fakeStore();
  store.tags.set("products", { expired: 500 });
  seedPage(store, "index", { tags: "products", lastModified: 1_000 });

  const entry = await new OcelCacheHandler().get("/", { kind: "APP_PAGE" });

  expect(entry).not.toBeNull();
});

// revalidateTag with a duration marks the tag stale now but sets expiry in the
// future, and Next's rule is that a not-yet-reached expiry leaves entries usable.
test("leaves entries usable until a future expiry actually arrives", async () => {
  const store = fakeStore();
  seedPage(store, "index", { tags: "products", lastModified: 1_000 });

  await new OcelCacheHandler().revalidateTag("products", { expire: 3600 });

  const record = store.tags.get("products");
  expect(record?.stale).toBeGreaterThan(0);
  expect(record?.expired).toBeGreaterThan(Date.now());
  expect(await new OcelCacheHandler().get("/", { kind: "APP_PAGE" })).not.toBeNull();
});

test("untagged entries never hit the tag store", async () => {
  const store = fakeStore();
  seedPage(store, "index");
  let reads = 0;
  const inner = store.readTags;
  store.readTags = async (t) => {
    reads++;
    return inner(t);
  };

  await new OcelCacheHandler().get("/", { kind: "APP_PAGE" });

  expect(reads).toBe(0);
});

// Next does not wrap get() in a try/catch, so a throw would surface as a render
// error. A cache outage must degrade to a miss instead.
test("reports a miss rather than throwing when the store fails", async () => {
  const store = fakeStore();
  seedPage(store, "index");
  store.breakReads();

  await expect(
    new OcelCacheHandler().get("/", { kind: "APP_PAGE" }),
  ).resolves.toBeNull();
});

test("round-trips a page written by set through get", async () => {
  fakeStore();
  const handler = new OcelCacheHandler();

  const deferred = await invocation(() =>
    handler.set(
      "/blog",
      {
        kind: "APP_PAGE",
        // On set, Next hands html over as a RenderResult, not a string.
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

// The per-variant rscHeaders/segmentHeaders exist only in the build's prerender
// output; Next's runtime set() payload has just a single page-level headers map.
// A revalidation rewrite must carry the build-seeded values forward, or the edge
// stops seeing the segment cache markers and silently drops PPR.
test("preserves build-seeded variant headers across a revalidation write", async () => {
  const store = fakeStore();
  store.entries.set("blog", {
    lastModified: 1_000,
    value: {
      kind: "APP_PAGE",
      html: "<html>seed</html>",
      status: 200,
      headers: {},
      rscHeaders: { "content-type": "text/x-component" },
      segmentHeaders: {
        "content-type": "text/x-component",
        "x-nextjs-postponed": "2",
      },
    },
  });
  const handler = new OcelCacheHandler();

  const deferred = await invocation(() =>
    handler.set(
      "/blog",
      {
        kind: "APP_PAGE",
        html: { toUnchunkedString: () => "<html>fresh</html>" },
        status: 200,
        headers: { "x-next-cache-tags": "posts" },
        segmentData: new Map([["/_tree", Buffer.from("TREE")]]),
      },
      {},
    ),
  );
  await Promise.all(deferred);

  const rewritten = store.entries.get("blog");
  expect(rewritten?.value.html).toBe("<html>fresh</html>");
  expect(rewritten?.value.rscHeaders).toEqual({ "content-type": "text/x-component" });
  expect(rewritten?.value.segmentHeaders).toEqual({
    "content-type": "text/x-component",
    "x-nextjs-postponed": "2",
  });
});

// A first-ever write for an on-demand route has no prior entry to carry from; it
// must still write rather than fail on the missing read.
test("writes without variant headers when no prior entry exists", async () => {
  const store = fakeStore();
  const handler = new OcelCacheHandler();

  const deferred = await invocation(() =>
    handler.set(
      "/blog",
      {
        kind: "APP_PAGE",
        html: { toUnchunkedString: () => "<html>fresh</html>" },
        status: 200,
        headers: {},
      },
      {},
    ),
  );
  await Promise.all(deferred);

  const written = store.entries.get("blog");
  expect(written?.value.html).toBe("<html>fresh</html>");
  expect(written?.value.segmentHeaders).toBeUndefined();
});

// The entry now lands in the store the edge reads, which is a cross-internet
// PUT. Resolving while that write is stalled is the whole assertion: a write on
// the request path would hang here instead.
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

// The value has to be read out of `data` on the request path: it carries a live
// RenderResult that does not outlive the request that produced it.
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

// Fetch entries are told their tags per request, unlike page kinds.
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

// Fetch bodies are upstream response data and stay in the provider's own bucket,
// which route entries do not when a substrate adopts an edge store. A write that
// landed in the entry store would leak them to the edge, and would read back as
// a miss besides — so assert the split explicitly in both directions.
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

// Next's own revalidateTag spreads the existing record before applying updates,
// so a later duration-based call must not drop an expiry set by an earlier
// invalidation. Dropping it would make an already-invalidated tag look fresh and
// serve stale content again.
test("marking a tag stale preserves an expiry set earlier", async () => {
  const store = fakeStore();
  seedPage(store, "index", { tags: "products", lastModified: 1_000 });
  const handler = new OcelCacheHandler();

  await handler.revalidateTag("products");
  const expiredAfterFirst = store.tags.get("products")?.expired;

  // durations present but no expire: sets stale only, and must leave expired be.
  await handler.revalidateTag("products", {});

  expect(store.tags.get("products")?.expired).toBe(expiredAfterFirst);
  expect(store.tags.get("products")?.stale).toBeGreaterThan(0);
  expect(await handler.get("/", { kind: "APP_PAGE" })).toBeNull();
});
