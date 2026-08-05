import {
  areTagsExpired,
  readableSnapshot,
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

// The subset of the Cache API the snapshot read fronts itself with. `delete` is
// optional because a front that cannot purge is still a usable read-through
// cache: it costs one TTL of staleness, never a wrong answer.
export interface SnapshotCache {
  match(request: Request): Promise<Response | undefined>;
  put(request: Request, response: Response): Promise<void>;
  delete?(request: Request): Promise<boolean>;
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
// These TTLs are the entire delay this design adds to an invalidation, because
// the publisher republishes on every revalidateTag — so an invalidation raised
// at the origin reaches a PoP within one TTL of being raised.
const snapshotTtlSeconds = 10;
const snapshotMemoMs = 1_000;

// Keyed by the binding itself, which is one stable object for the life of an
// isolate. Keying on the binding rather than on module state is also what keeps
// the memo from leaking between tests.
//
// A null snapshot is memoized like any other: an unreadable replica is the case
// that costs the most — every tagged route on the isolate falls open to the
// origin — so it is exactly the one that must not also pay a store read per
// call. The memo window bounds how long the isolate waits to notice a repair.
const snapshotMemo = new WeakMap<
  ObjectStoreReader,
  { at: number; snapshot: TagSnapshot | null }
>();

// Only a writer that has just republished the replica needs this: the memo
// window is what would otherwise answer that writer's own next read from the
// snapshot it just replaced.
// Reads in flight, so a burst of tagged requests arriving on one isolate before
// its memo is filled shares a single round trip instead of issuing one store
// read apiece. The memo above dedupes across time; this dedupes across
// concurrency, which is the axis a colo's traffic actually arrives on.
//
// A read that rejects is not left here: the next caller starts a new one. It is
// also not memoized — only the read's own answer is, and a throw never reaches
// that line — so a joiner gets exactly what a solo caller would have got, and a
// store blip costs one round trip's worth of callers, not a memo window's.
const snapshotReads = new WeakMap<ObjectStoreReader, Promise<TagSnapshot | null>>();

export function dropSnapshotMemo(store: ObjectStoreReader): void {
  snapshotMemo.delete(store);
}

// An invalidation raised through this worker was already published by the origin
// that answered it, so every layer fronting the replica is known-stale. The memo
// alone is not enough: the PoP copy outlives the isolate and is what every other
// isolate in the colo reads, so it is the layer that would otherwise answer the
// raiser's own next request from before their write.
//
// Per-colo by construction — a worker cannot reach another PoP's cache — and
// that is exactly the guarantee worth having, because the visitor who raised the
// invalidation is served by the colo that just purged. Other colos converge on
// their own TTL, as they did before.
export async function invalidateSnapshot(
  cfg: { isrPrefix: string },
  deps: TagClockDeps,
): Promise<void> {
  dropSnapshotMemo(deps.store);
  try {
    const key = tagSnapshotKey(cfg.isrPrefix);
    await deps.snapshotCache?.delete?.(new Request(snapshotCacheUrl(key)));
  } catch {
    // A purge that fails costs this colo one TTL of staleness, not a wrong
    // answer: DynamoDB remains the authoritative clock either way.
  }
}

// createTagClock reads the build's tag-clock replica, fronted by the per-isolate
// memo and the PoP Cache API exactly as the interception path did before the
// split. The memo is keyed on the store binding, so two clocks over the same
// binding share it — the caller need not thread one instance everywhere.
export function createTagClock(
  cfg: { isrPrefix: string },
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
  cfg: { isrPrefix: string },
  deps: TagClockDeps,
  now: number,
): Promise<Map<string, TagRecord> | null> {
  const snapshot = await readSnapshot(cfg, deps, now);
  return snapshot && new Map(Object.entries(snapshot.records));
}

// readSnapshot returns the build's tag-clock replica, or null when there is no
// replica this reader can read at all — missing, torn, or written in a format
// this worker predates. Those fall open to the origin, which is always correct.
//
// How current the replica is, is the publisher's business and not checked here.
// A reader that second-guessed it would have to decide what "too old" means
// without knowing whether anything had happened to publish, and the only honest
// answer to that is the one the writer already gave by leaving the object as it
// is: nothing has changed since.
async function readSnapshot(
  cfg: { isrPrefix: string },
  deps: TagClockDeps,
  now: number,
): Promise<TagSnapshot | null> {
  const memoized = snapshotMemo.get(deps.store);
  if (memoized && now - memoized.at < snapshotMemoMs) return memoized.snapshot;

  const inFlight = snapshotReads.get(deps.store);
  if (inFlight !== undefined) return inFlight;

  const read = fillSnapshot(cfg, deps, now).finally(() => snapshotReads.delete(deps.store));
  snapshotReads.set(deps.store, read);
  return read;
}

async function fillSnapshot(
  cfg: { isrPrefix: string },
  deps: TagClockDeps,
  now: number,
): Promise<TagSnapshot | null> {
  const key = tagSnapshotKey(cfg.isrPrefix);
  const cacheRequest = new Request(snapshotCacheUrl(key));
  const cached = await matchSnapshot(deps.snapshotCache, cacheRequest);

  const body = cached ?? (await storeText(deps.store, key));
  const snapshot = body === null ? null : readableSnapshot(parseJson<TagSnapshot>(body));

  if (snapshot && cached === null && deps.snapshotCache) {
    await deps.snapshotCache.put(
      cacheRequest,
      new Response(body!, {
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
