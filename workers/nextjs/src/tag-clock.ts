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
export const snapshotTtlSeconds = 10;
const snapshotMemoMs = 1_000;

// How far below the ceiling a copy's lifetime may be drawn. A flat max-age is a
// shared schedule: every cache shard in the colo holding the copy was filled in
// the same cycle, so all of them lapse at the same instant, all of them read the
// replica, and all of them write it back. That fan-in is bounded by nothing but
// the shard count, it recurs every TTL, and no tag has to change for it to
// happen — unlike everything else in this epic it is the steady-state cost of
// serving tagged traffic at all.
//
// Drawn, the shards' lapse instants diffuse apart instead of staying phase
// locked, and the fan-in at any one instant flattens toward one. This is the
// argument admissionJitterMs makes for claims (workers/nextjs/src/cache.ts),
// applied to an expiry rather than to an attempt.
//
// Drawn DOWNWARD, so it costs no staleness: the ceiling an invalidation waits
// out stays exactly snapshotTtlSeconds, and the mean falls from 10s to 8.5s.
// Seconds are integral because delta-seconds is, which leaves four phases —
// enough, because the walk compounds every cycle rather than being redrawn from
// the same offset.
export const snapshotJitterSeconds = 4;

// Exported so the draw is asserted directly rather than through a header, and
// so the production default is what a test can exercise. It is the only source
// of the copy's lifetime: there is no injected alternative to leave unwired.
export function snapshotMaxAgeSeconds(): number {
  const drawn = snapshotTtlSeconds - Math.floor(Math.random() * snapshotJitterSeconds);
  return Math.max(1, drawn);
}

// One cell per replica, holding both the answer this isolate last read and the
// read it currently has in flight. The memo dedupes across time; the in-flight
// read dedupes across concurrency, which is the axis a colo's traffic actually
// arrives on.
//
// A null snapshot is memoized like any other: an unreadable replica is the case
// that costs the most — every tagged route on the isolate falls open to the
// origin — so it is exactly the one that must not also pay a store read per
// call. A read that rejects reaches neither field, so a joiner gets exactly
// what a solo caller would have got and a store blip costs one round trip's
// worth of callers rather than a memo window's.
interface SnapshotCell {
  memo?: { at: number; snapshot: TagSnapshot | null };
  read?: Promise<TagSnapshot | null>;
}

// Keyed by the binding, then by the build prefix. The binding is one stable
// object for the life of an isolate — and keying on it rather than on module
// state is what keeps the state from leaking between tests — but it is NOT one
// per build: a deploy rollover puts two prefixes through the same binding on
// one isolate, and build N+1's reader must never be answered from build N's
// replica.
const snapshotCells = new WeakMap<ObjectStoreReader, Map<string, SnapshotCell>>();

function snapshotCell(cfg: { isrPrefix: string }, store: ObjectStoreReader): SnapshotCell {
  let byPrefix = snapshotCells.get(store);
  if (!byPrefix) snapshotCells.set(store, (byPrefix = new Map()));
  let cell = byPrefix.get(cfg.isrPrefix);
  if (!cell) byPrefix.set(cfg.isrPrefix, (cell = {}));
  return cell;
}

function current(
  cfg: { isrPrefix: string },
  store: ObjectStoreReader,
): SnapshotCell | undefined {
  return snapshotCells.get(store)?.get(cfg.isrPrefix);
}

// Dropping the whole cell is what abandons a read in flight, and the two are
// one act rather than two: an in-flight read is a read of the replica as it was
// BEFORE the write that prompted the drop, so its answer must not become this
// isolate's memo either. It still settles, and the caller that started it still
// gets its own answer — it just writes into a cell nothing can reach any more.
export function dropSnapshotMemo(
  cfg: { isrPrefix: string },
  store: ObjectStoreReader,
): void {
  snapshotCells.get(store)?.delete(cfg.isrPrefix);
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
  dropSnapshotMemo(cfg, deps.store);
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
  const cell = snapshotCell(cfg, deps.store);
  if (cell.memo && now - cell.memo.at < snapshotMemoMs) return cell.memo.snapshot;
  if (cell.read) return cell.read;

  const read = fillSnapshot(cfg, deps, now, cell).finally(() => {
    cell.read = undefined;
  });
  cell.read = read;
  return read;
}

async function fillSnapshot(
  cfg: { isrPrefix: string },
  deps: TagClockDeps,
  now: number,
  cell: SnapshotCell,
): Promise<TagSnapshot | null> {
  const key = tagSnapshotKey(cfg.isrPrefix);
  const cacheRequest = new Request(snapshotCacheUrl(key));
  const cached = await matchSnapshot(deps.snapshotCache, cacheRequest);

  const body = cached ?? (await storeText(deps.store, key));
  const snapshot = body === null ? null : readableSnapshot(parseJson<TagSnapshot>(body));

  // The PoP copy is the one effect of this read that outlives an abandonment,
  // so it is the one that has to ask whether it was abandoned: re-putting a
  // body a purge has already deleted resurrects it colo-wide, for every isolate
  // in the colo, for a whole TTL. The memo needs no such check — it is written
  // into this read's own cell, which the drop has already made unreachable.
  if (snapshot && cached === null && deps.snapshotCache && current(cfg, deps.store) === cell) {
    await deps.snapshotCache.put(
      cacheRequest,
      new Response(body!, {
        headers: { "cache-control": `max-age=${snapshotMaxAgeSeconds()}` },
      }),
    );
  }
  cell.memo = { at: now, snapshot };
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
