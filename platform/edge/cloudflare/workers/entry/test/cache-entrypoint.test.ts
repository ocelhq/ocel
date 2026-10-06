import { createExecutionContext, env } from "cloudflare:test";
import { type TagRecord, type TagSnapshot, tagSnapshotKey } from "@framework/next-cache";
import { beforeEach, expect, it } from "vitest";

import {
  CacheEntrypoint,
  createEdgeCache,
  type FetchEntryStore,
  tagRaiser,
} from "../src/cache-entrypoint";
import type { Env } from "../src/index";
import type { ObjectStoreReader } from "../src/tag-clock";

declare module "cloudflare:test" {
  interface ProvidedEnv {
    TAG_SNAPSHOT_STORE: R2Bucket;
  }
}

function snapshotBucket(): ObjectStoreReader {
  return { get: (key) => env.TAG_SNAPSHOT_STORE.get(key) };
}

function raiseRecorder(
  land: (records: Record<string, TagRecord>) => Promise<void> = async () => {},
) {
  const raises: { scope: string; records: Record<string, TagRecord> }[] = [];
  return {
    raises,
    raise: async (scope: string, records: Record<string, TagRecord>) => {
      raises.push({ scope, records });
      await land(records);
    },
  };
}

function waitUntilRecorder() {
  const pending: Promise<unknown>[] = [];
  return { pending, waitUntil: (promise: Promise<unknown>) => void pending.push(promise) };
}

const entry = (over: Record<string, unknown> = {}) => ({
  lastModified: 1_000,
  value: { kind: "FETCH", data: { body: "hi" }, ...over },
});

let scopes = 0;
let scope = "";
beforeEach(() => {
  scope = `prod/proj/app/r${String(scopes++).padStart(8, "0")}/isr`;
});

async function seedSnapshot(records: TagSnapshot["records"], deployedAt = 0): Promise<void> {
  await env.TAG_SNAPSHOT_STORE.put(
    tagSnapshotKey(scope),
    JSON.stringify({ version: 1, deployedAt, generatedAt: 0, records }),
  );
}

const entryKey = (key: string, prefix = scope) => `${prefix}/fetch-cache/${key}.cache.json`;

async function seedEntry(value: unknown, key = "abc123"): Promise<void> {
  await env.TAG_SNAPSHOT_STORE.put(entryKey(key), JSON.stringify(value));
}

async function storedEntry(key = "abc123"): Promise<unknown> {
  return (await env.TAG_SNAPSHOT_STORE.get(entryKey(key)))?.json();
}

function entryStore(over: Partial<FetchEntryStore> = {}) {
  const puts: { key: string; contentType?: string }[] = [];
  const store: FetchEntryStore = {
    get: (key) => env.TAG_SNAPSHOT_STORE.get(key),
    put: async (key, value, options) => {
      puts.push({ key, contentType: options?.httpMetadata?.contentType });
      return env.TAG_SNAPSHOT_STORE.put(key, value, options);
    },
    ...over,
  };
  return { puts, store };
}

function cacheWith(
  over: {
    scope?: string;
    entries?: FetchEntryStore;
    store?: ObjectStoreReader;
    raise?: (scope: string, records: Record<string, TagRecord>) => Promise<void>;
    waitUntil?: (p: Promise<unknown>) => void;
    now?: () => number;
  } = {},
) {
  return createEdgeCache({
    scope: over.scope ?? scope,
    entries: over.entries ?? entryStore().store,
    snapshots: over.store ?? snapshotBucket(),
    raise: over.raise ?? raiseRecorder().raise,
    waitUntil: over.waitUntil ?? (() => {}),
    now: over.now ?? (() => 5_000),
  });
}

it("reads a fetch entry from the cache store under the deployment's prefix", async () => {
  const stored = entry();
  await seedEntry(stored);

  expect(await cacheWith().fetchGet(scope, "abc123", [])).toEqual(stored);
});

it("misses on an absent object", async () => {
  expect(await cacheWith().fetchGet(scope, "abc123", [])).toBeNull();
});

it("misses rather than throwing when the store is unreachable", async () => {
  const { store } = entryStore({
    get: async () => {
      throw new Error("r2 down");
    },
  });
  expect(await cacheWith({ entries: store }).fetchGet(scope, "abc123", [])).toBeNull();
});

it("misses when a tag was invalidated after the entry was written", async () => {
  await seedSnapshot({ posts: { expired: 2_000 } });
  await seedEntry(entry());
  expect(await cacheWith().fetchGet(scope, "abc123", ["posts"])).toBeNull();
});

it("serves an entry whose tags are clean", async () => {
  await seedSnapshot({ posts: { expired: 500 } });
  await seedEntry(entry());
  expect(await cacheWith().fetchGet(scope, "abc123", ["posts"])).toEqual(entry());
});

it("evaluates the tags the entry itself recorded, not only the caller's", async () => {
  await seedSnapshot({ authors: { expired: 2_000 } });
  await seedEntry(entry({ tags: ["authors"] }));
  expect(await cacheWith().fetchGet(scope, "abc123", ["posts"])).toBeNull();
});

it("misses when the tag snapshot cannot be trusted", async () => {
  await seedEntry(entry());
  expect(await cacheWith().fetchGet(scope, "abc123", ["posts"])).toBeNull();
  expect(await cacheWith().fetchGet(scope, "abc123", [])).toEqual(entry());
});

it("returns from a write before the object lands, and writes behind it", async () => {
  let release = () => {};
  const blocked = new Promise<void>((resolve) => (release = resolve));
  const { puts, store } = entryStore();
  const entries: FetchEntryStore = {
    ...store,
    put: async (key, value, options) => {
      await blocked;
      return store.put(key, value, options);
    },
  };
  const { pending, waitUntil } = waitUntilRecorder();

  await cacheWith({ entries, waitUntil }).fetchSet(scope, "abc123", entry(), ["posts"]);

  expect(pending).toHaveLength(1);
  expect(await storedEntry()).toBeUndefined();
  expect(puts).toHaveLength(0);
  release();
  await Promise.all(pending);

  expect(puts).toEqual([{ key: entryKey("abc123"), contentType: "application/json" }]);
  expect(await storedEntry()).toEqual({
    ...entry(),
    value: { ...entry().value, tags: ["posts"] },
  });
});

it("skips an oversized entry rather than failing the render", async () => {
  const { puts, store } = entryStore();
  const { pending, waitUntil } = waitUntilRecorder();
  const huge = entry({ data: { body: "x".repeat(3 * 1024 * 1024) } });

  await expect(
    cacheWith({ entries: store, waitUntil }).fetchSet(scope, "abc123", huge, []),
  ).resolves.toBeUndefined();
  expect(puts).toHaveLength(0);
  expect(pending).toHaveLength(0);
});

it.each(["APP_PAGE", "APP_ROUTE", "PAGES", undefined])(
  "refuses to store a %s entry rather than corrupting the fetch cache",
  async (kind) => {
    const { puts, store } = entryStore();
    const { pending, waitUntil } = waitUntilRecorder();

    await expect(
      cacheWith({ entries: store, waitUntil }).fetchSet(scope, "abc123", entry({ kind }), []),
    ).rejects.toThrow(/fetch entries only/);
    expect(puts).toHaveLength(0);
    expect(pending).toHaveLength(0);
  },
);

it("does not surface a failed write to the caller", async () => {
  const { store } = entryStore({
    put: async () => {
      throw new Error("429 Too Many Requests");
    },
  });
  const { pending, waitUntil } = waitUntilRecorder();
  await cacheWith({ entries: store, waitUntil }).fetchSet(scope, "abc123", entry(), []);
  await expect(Promise.all(pending)).resolves.toBeDefined();
});

it("raises every invalidated tag through the writer, under this deployment's prefix", async () => {
  const writer = raiseRecorder();
  await cacheWith({ raise: writer.raise }).revalidateTags(scope, ["posts", "authors"], {
    expire: 60,
  });

  expect(writer.raises).toEqual([
    {
      scope,
      records: {
        posts: { stale: 5_000, expired: 65_000 },
        authors: { stale: 5_000, expired: 65_000 },
      },
    },
  ]);
});

it("sees its own invalidation on the very next read", async () => {
  const stored = entry();
  await seedEntry(stored);
  const writer = raiseRecorder((records) => seedSnapshot(records));
  const cache = cacheWith({ store: snapshotBucket(), raise: writer.raise });

  await seedSnapshot({ posts: { expired: 500 } });
  expect(await cache.fetchGet(scope, "abc123", ["posts"])).toEqual(stored);

  await cache.revalidateTags(scope, ["posts"]);
  expect(await cache.fetchGet(scope, "abc123", ["posts"])).toBeNull();
});

it("drops its snapshot memo even when the raise failed", async () => {
  const stored = entry();
  await seedEntry(stored);
  const cache = cacheWith({
    store: snapshotBucket(),
    raise: async () => {
      throw new Error("429");
    },
  });

  await seedSnapshot({ posts: { expired: 500 } });
  expect(await cache.fetchGet(scope, "abc123", ["posts"])).toEqual(stored);

  await expect(cache.revalidateTags(scope, ["posts"])).rejects.toThrow("429");
  await seedSnapshot({ posts: { expired: 5_000 } });
  expect(await cache.fetchGet(scope, "abc123", ["posts"])).toBeNull();
});

it("posts a raise to the writer under this deploy's own write secret", async () => {
  const posted: Request[] = [];
  const raise = tagRaiser(
    {
      fetch: async (request: Request) => {
        posted.push(request);
        return new Response(null, { status: 204 });
      },
    },
    "write-secret",
  );

  await raise(scope, { posts: { expired: 5_000 } });

  expect(posted).toHaveLength(1);
  expect(new URL(posted[0].url).pathname).toBe(`/${scope}/tags`);
  expect(posted[0].method).toBe("POST");
  expect(posted[0].headers.get("authorization")).toBe("Bearer write-secret");
  expect(await posted[0].json()).toEqual({ records: { posts: { expired: 5_000 } } });
});

it.each([429, 401, 500])("reports a raise the writer answered with %i", async (status) => {
  const raise = tagRaiser({ fetch: async () => new Response(null, { status }) }, "write-secret");
  await expect(raise(scope, { posts: { expired: 5_000 } })).rejects.toThrow(String(status));
});

it("reports a raise it has no writer to make", async () => {
  await expect(tagRaiser(undefined, "write-secret")(scope, {})).rejects.toThrow(/no isr writer/);
  await expect(
    tagRaiser({ fetch: async () => new Response(null) }, undefined)(scope, {}),
  ).rejects.toThrow(/no isr writer/);
});

const otherScope = "prod/proj/other/r00000000/isr";

it("answers no entry for a scope other than the one its deployment bound", async () => {
  await env.TAG_SNAPSHOT_STORE.put(entryKey("abc123", otherScope), JSON.stringify(entry()));
  const reads: string[] = [];
  const { store } = entryStore({
    get: async (key) => {
      reads.push(key);
      return env.TAG_SNAPSHOT_STORE.get(key);
    },
  });
  expect(await cacheWith({ entries: store }).fetchGet(otherScope, "abc123", [])).toBeNull();
  expect(reads).toHaveLength(0);
});

it("writes nothing for a scope other than the one its deployment bound", async () => {
  const { puts, store } = entryStore();
  const { pending, waitUntil } = waitUntilRecorder();

  await expect(
    cacheWith({ entries: store, waitUntil }).fetchSet(otherScope, "abc123", entry(), []),
  ).resolves.toBeUndefined();
  expect(puts).toHaveLength(0);
  expect(pending).toHaveLength(0);
});

it("refuses to invalidate tags of a scope other than the one its deployment bound", async () => {
  const writer = raiseRecorder();

  await expect(
    cacheWith({ raise: writer.raise }).revalidateTags(otherScope, ["posts"]),
  ).rejects.toThrow(`bound to ${scope}, not ${otherScope}`);
  expect(writer.raises).toHaveLength(0);
});

it("answers like an empty cache when its deployment bound no scope", async () => {
  const entrypoint = new CacheEntrypoint(createExecutionContext(), {
    OCEL_CACHE_STORE: env.TAG_SNAPSHOT_STORE,
  } as Env);

  expect(await entrypoint.fetchGet(scope, "abc123", [])).toBeNull();
  await entrypoint.fetchSet(scope, "abc123", entry(), []);
  await entrypoint.revalidateTags(scope, ["posts"]);
});

it("answers like an empty cache on a bootstrap that binds no coordinates", async () => {
  const entrypoint = new CacheEntrypoint(createExecutionContext(), {} as Env);

  expect(await entrypoint.fetchGet(scope, "abc123", ["posts"])).toBeNull();
  await entrypoint.fetchSet(scope, "abc123", entry(), ["posts"]);
  await entrypoint.revalidateTags(scope, ["posts"]);
});

it("records an invalidation only by raising it to the isr-writer", async () => {
  const writer = raiseRecorder();
  await cacheWith({ raise: writer.raise }).revalidateTags(scope, ["posts"], { expire: 60 });

  expect(writer.raises).toEqual([{ scope, records: { posts: { stale: 5_000, expired: 65_000 } } }]);
  expect(writer.raises).toHaveLength(1);
});

it("surfaces a refused raise", async () => {
  await expect(
    cacheWith({
      raise: async () => {
        throw new Error("429");
      },
    }).revalidateTags(scope, ["posts"]),
  ).rejects.toThrow("429");
});

it("serves fetch entries with no AWS bindings at all", async () => {
  await seedEntry(entry());
  const ctx = Object.assign(createExecutionContext(), { props: { scope } });
  const entrypoint = new CacheEntrypoint(ctx, { OCEL_CACHE_STORE: env.TAG_SNAPSHOT_STORE } as Env);

  expect(await entrypoint.fetchGet(scope, "abc123", [])).toEqual(entry());
});
