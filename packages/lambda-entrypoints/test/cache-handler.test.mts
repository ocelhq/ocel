import { afterEach, expect, test } from "vitest";
import OcelCacheHandler from "../src/next/cache-handler.mjs";
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
  const tags = new Map<string, TagRecord>();
  let failReads = false;

  const store: CacheStore & {
    entries: typeof entries;
    tags: typeof tags;
    breakReads(): void;
  } = {
    entries,
    tags,
    breakReads() {
      failReads = true;
    },
    async readEntry(key) {
      if (failReads) throw new Error("s3 is down");
      return entries.get(key) ?? null;
    },
    async writeEntry(key, entry) {
      entries.set(key, entry);
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
      for (const n of names) tags.set(n, record);
    },
  };
  OcelCacheHandler.store = store;
  return store;
}

afterEach(() => {
  OcelCacheHandler.store = undefined;
});

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

  await handler.set(
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
  );

  const entry = await handler.get("/blog", { kind: "APP_PAGE" });
  expect(entry?.value.html).toBe("<html>blog</html>");
  expect(entry?.value.rscData.toString()).toBe("BLOG-RSC");
  expect(entry?.value.segmentData.get("/_tree").toString()).toBe("TREE");
});

// Fetch entries are told their tags per request, unlike page kinds.
test("takes fetch tags from the request context", async () => {
  const store = fakeStore();
  store.entries.set("__fetch__/abc", {
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
