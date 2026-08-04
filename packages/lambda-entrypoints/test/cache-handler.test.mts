import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
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
  // What a `set` costs the ISR writer, counted rather than inferred: the whole
  // point of the projection is that a rewrite is one round trip.
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
      calls.readEntry++;
      if (failReads) throw new Error("s3 is down");
      return entries.get(key) ?? null;
    },
    async writeEntry(key, entry) {
      calls.writeEntry++;
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
    // Rejects a repeated tag the way BatchGetItem does. A fake that quietly
    // tolerates duplicates is more permissive than the service it stands for,
    // and every caller's list would pass here while failing in production.
    async readTags(names) {
      if (new Set(names).size !== names.length) {
        throw new Error("Provided list of item keys contains duplicates");
      }
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
  OcelCacheHandler.variantHeaders = undefined;
  delete process.env.LAMBDA_TASK_ROOT;
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

// Next replays the entry's headers verbatim and never overrides a content-type
// that is already set, so an RSC request answered with the html variant's
// headers goes out as text/html — which the client router reads as "not flight
// data" and answers with a full document reload instead of a soft navigation.
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

// The production case: for a prerendered route Next deletes the flight headers
// off the live request object before it builds the incremental cache, so `rsc`
// is already gone by the time the handler is constructed. Only the membrane's
// mark, set before Next ran, still says what the client asked for.
test("negotiates on the membrane's mark once Next has stripped the flight headers", async () => {
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

// The mark must not reach application code: Next's `headers()` and everything
// else that reads a request's headers enumerates or serializes string keys. It
// does survive a spread, which is what keeps it readable if Next clones.
test("the membrane's mark is invisible to header enumeration", () => {
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

// An entry written before per-variant capture has no rscHeaders. Serving it
// with the html content-type is the same reload bug, so drop the header and let
// Next derive it from the flight payload it is about to send.
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

// The build manifest names only the paths the build prerendered, so for a path
// generated on demand this write is the one place the route's freshness window
// is ever known. Dropping it leaves the edge with nothing to age the entry by.
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

// A fetch entry carries its own revalidate inside the value; the route-level
// window means nothing to it, and Next never sends one.
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

// The build ships route -> {rscHeaders, segmentHeaders} beside the function.
// This is that file, as the adapter's projection writes it.
function seedProjection(
  projection: Record<string, Record<string, unknown>>,
): void {
  OcelCacheHandler.variantHeaders = projection;
}

// What the adapter lays beside the function, spelled out rather than imported:
// this package cannot load the adapter (it needs `next`) and the adapter cannot
// load this handler (it needs the AWS SDK), so both suites pin the name against
// this literal and a rename that reaches only one side fails here.
const variantHeadersFile = "variant-headers.json";

// The projection on disk where the handler reads it from — the only way to drive
// what an actual bundle hands the loader. `null` writes no file at all.
function bundleProjection(contents: string | null): void {
  bundleRoot ??= mkdtempSync(join(tmpdir(), "ocel-bundle-"));
  process.env.LAMBDA_TASK_ROOT = bundleRoot;
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

// The per-variant rscHeaders/segmentHeaders exist only in the build's prerender
// output; Next's runtime set() payload has just a single page-level headers map.
// A revalidation rewrite must reseed them, or the edge stops seeing the segment
// cache markers and silently drops PPR.
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

// The projection is what makes the rewrite a single round trip: it replaces a
// full GetObject of the prior entry — the whole html+RSC+PPR payload, pulled
// back through the ISR writer for two small header maps.
test("a revalidation write is one write and no read", async () => {
  const store = fakeStore();
  seedProjection({ blog: { rscHeaders: { "content-type": "text/x-component" } } });

  await revalidate("/blog");

  expect(store.calls).toEqual({ readEntry: 0, writeEntry: 1 });
});

// A route the build never prerendered — generated on demand — is in no
// projection. It has no variant headers to reseed and must still be written.
test("writes a route the projection does not name", async () => {
  const store = fakeStore();
  seedProjection({});

  await revalidate("/blog");

  const written = store.entries.get("blog");
  expect(written?.value.html).toBe("<html>fresh</html>");
  expect(written?.value.rscHeaders).toBeUndefined();
  expect(written?.value.segmentHeaders).toBeUndefined();
});

// The two things the reseeded headers exist for, end to end from the write that
// would otherwise have dropped them: the edge replays segmentHeaders to answer a
// segment prefetch (an entry without them falls open and serves no segment), and
// an RSC request is negotiated onto rscHeaders rather than going out as html.
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
  expect(written.value.segmentData["/_tree"]).toBe(
    Buffer.from("TREE").toString("base64"),
  );

  const served = await new OcelCacheHandler({
    _requestHeaders: { rsc: "1" },
  }).get("/blog", { kind: "APP_PAGE" });
  expect(served?.value.headers["content-type"]).toBe("text/x-component");
});

// Whatever the bundle holds under variant-headers.json, a write must still land.
// The projection is a reseeding convenience; a bundle that cannot supply one has
// nothing to reseed from anyway, and a route that stops caching for the life of
// a deploy — silently, with no log — is the one outcome that is worse than
// serving without the variant headers.
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
  test(`writes the entry when the bundle holds ${name}`, async () => {
    const store = fakeStore();
    bundleProjection(contents);

    await revalidate("/blog");

    const written = store.entries.get("blog");
    expect(written?.value.html).toBe("<html>fresh</html>");
    expect(written?.value.rscHeaders).toBeUndefined();
    expect(written?.value.segmentHeaders).toBeUndefined();
  });
}

// An unreadable projection is loaded once, like a readable one. Re-reading it on
// every set would put a synchronous file read on every APP_PAGE write.
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

// A stored fetch entry records the tags it was written under, and its reader
// passes the same ones back in — so the tag list this handler builds names them
// twice unless it dedupes. BatchGetItem rejects a repeated key, and get()
// swallows the throw into a miss, which makes a tagged entry unreadable for as
// long as it carries the tag.
test("reads back a fetch entry whose tags the request also names", async () => {
  const store = fakeStore();
  const handler = new OcelCacheHandler();

  // Written through set() rather than seeded, because set() storing ctx.tags on
  // the entry is what makes the reader's list repeat them.
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
