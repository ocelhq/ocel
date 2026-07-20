import {
  areTagsExpired,
  tagSnapshotKey,
  type TagRecord,
  type TagSnapshot,
} from "@ocel/next-cache";

// One stored object, as the R2 binding hands it back.
export interface StoredObject {
  etag?: string;
  text(): Promise<string>;
}

// The cache store as this file needs it: keyed reads, null for a miss. The
// Cloudflare R2 binding satisfies it directly, so nothing here names an edge.
export interface ObjectStoreReader {
  get(key: string): Promise<StoredObject | null>;
}

// The subset of the Cache API the snapshot read fronts itself with.
export interface SnapshotCache {
  match(request: Request): Promise<Response | undefined>;
  put(request: Request, response: Response): Promise<void>;
}

export interface TagClockDeps {
  store: ObjectStoreReader;
  snapshotCache?: SnapshotCache;
  waitUntil?: (promise: Promise<unknown>) => void;
}

export type TagVerdict = boolean | "untrusted";

export interface TagClock {
  // true: a tag invalidated the entry. false: trusted, none did.
  // "untrusted": the snapshot could not be read/trusted, so staleness is unknown.
  expired(tags: string[], timestamp: number, now: number): Promise<TagVerdict>;
  // Warms the isolate-shared snapshot memo so a following expired() in the
  // same request needs no store round-trip. Route-independent; its result is
  // consumed via expired(). Never throws to the caller.
  prime(now: number): Promise<unknown>;
}

// The tag-clock replica is read on every tagged interception, so it is fronted
// by two layers: the PoP-shared Cache API, and a per-isolate memo covering the
// burst one isolate serves inside a second.
//
// The TTL is the entire delay this design adds to an invalidation, because the
// publisher republishes on every revalidateTag — so an invalidation raised at
// the origin reaches a PoP within one TTL of being raised. Ten seconds buys a
// PoP's whole burst for one store read while staying far inside the publisher's
// five-minute trust window, so the window, not the cache, remains what bounds
// worst-case staleness.
const snapshotTtlSeconds = 10;
const snapshotMemoMs = 1_000;

// Keyed by the binding itself, which is one stable object for the life of an
// isolate. Keying on the binding rather than on module state is also what keeps
// the memo from leaking between tests.
const snapshotMemo = new WeakMap<
  ObjectStoreReader,
  { at: number; snapshot: TagSnapshot }
>();

// createTagClock reads the build's tag-clock replica, fronted by the per-isolate
// memo and the PoP Cache API exactly as the interception path did before the
// split. The memo is keyed on the store binding, so two clocks over the same
// binding share it — the caller need not thread one instance everywhere.
export function createTagClock(
  cfg: { prefix: string },
  deps: TagClockDeps,
): TagClock {
  return {
    async expired(tags, timestamp, now) {
      if (tags.length === 0) return false;
      try {
        const records = await snapshotRecords(cfg, deps, now);
        if (!records) return "untrusted";
        return areTagsExpired(tags, records, timestamp, now);
      } catch {
        // A store error is a slower miss, not a wrong answer: unknown never
        // serves, same as an absent or untrusted snapshot.
        return "untrusted";
      }
    },
    async prime(now) {
      try {
        return await snapshotRecords(cfg, deps, now);
      } catch {
        return null;
      }
    },
  };
}

// snapshotRecords resolves tag state from the tag clock as the last publisher
// left it, rather than a live read of the authoritative one.
async function snapshotRecords(
  cfg: { prefix: string },
  deps: TagClockDeps,
  now: number,
): Promise<Map<string, TagRecord> | null> {
  const snapshot = await readSnapshot(cfg, deps, now);
  return snapshot && new Map(Object.entries(snapshot.records));
}

// readSnapshot returns the build's tag-clock replica, or null whenever it cannot
// be trusted — missing, torn, stale, or written in a format this worker predates.
// Every one of those falls open to the origin, which wakes a Lambda, which
// republishes: the liveness loop repairs itself by being used.
async function readSnapshot(
  cfg: { prefix: string },
  deps: TagClockDeps,
  now: number,
): Promise<TagSnapshot | null> {
  const memoized = snapshotMemo.get(deps.store);
  if (memoized && now - memoized.at < snapshotMemoMs) {
    return usableSnapshot(memoized.snapshot, now);
  }

  const key = tagSnapshotKey(cfg.prefix);
  const cacheRequest = new Request(snapshotCacheUrl(key));
  const cached = await matchSnapshot(deps.snapshotCache, cacheRequest);

  const body = cached ?? (await storeText(deps.store, key));
  if (body === null) return null;

  const snapshot = usableSnapshot(parseJson<TagSnapshot>(body), now);
  if (!snapshot) return null;

  if (cached === null && deps.snapshotCache) {
    await deps.snapshotCache.put(
      cacheRequest,
      new Response(body, {
        headers: { "cache-control": `max-age=${snapshotTtlSeconds}` },
      }),
    );
  }
  snapshotMemo.set(deps.store, { at: now, snapshot });
  return snapshot;
}

// A PoP cache that is absent, inert, or erroring is a slower read, never a
// wrong one, so a miss and a failure are the same answer: go to the store.
async function matchSnapshot(
  cache: SnapshotCache | undefined,
  request: Request,
): Promise<string | null> {
  try {
    const hit = await cache?.match(request);
    return hit ? await hit.text() : null;
  } catch {
    return null;
  }
}

// A replica is trusted only inside the window its publisher declared, and only
// at a version this worker was written against. An unknown version is a format
// this reader cannot claim to understand, so it declines to guess — which is
// what lets the format change without a worker fleet misreading it.
function usableSnapshot(
  snapshot: TagSnapshot | null,
  now: number,
): TagSnapshot | null {
  if (snapshot?.version !== 1) return null;
  if (typeof snapshot.validUntil !== "number" || now >= snapshot.validUntil) {
    return null;
  }
  return snapshot.records && typeof snapshot.records === "object"
    ? snapshot
    : null;
}

export async function storeText(
  store: ObjectStoreReader,
  key: string,
): Promise<string | null> {
  const object = await store.get(key);
  return object ? object.text() : null;
}

export function parseJson<T>(body: string): T | null {
  try {
    return JSON.parse(body) as T;
  } catch {
    return null;
  }
}

// An object key's separators are path structure and have to survive into the
// URL, so each segment is encoded on its own rather than the key as a whole.
function encodeKeyPath(key: string): string {
  return key.split("/").map(encodeURIComponent).join("/");
}

// The Cache API keys on a URL, and the snapshot has none: it is read through a
// binding, not fetched. This synthesizes one from the object key, which already
// carries the build prefix, so two builds on one worker cannot collide.
function snapshotCacheUrl(key: string): string {
  return `https://isr.ocel/${encodeKeyPath(key)}`;
}
