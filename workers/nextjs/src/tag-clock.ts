import {
  areTagsExpired,
  readableSnapshot,
  tagSnapshotKey,
  type TagRecord,
  type TagSnapshot,
} from "@ocel/next-cache";

export interface StoredObject {
  etag?: string;
  text(): Promise<string>;
}

export interface ObjectStoreReader {
  get(key: string): Promise<StoredObject | null>;
}

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
  expired(tags: string[], timestamp: number, now: number): Promise<TagVerdict>;
  prime(now: number): Promise<unknown>;
}

export const snapshotTtlSeconds = 10;
const snapshotMemoMs = 1_000;

export const snapshotJitterSeconds = 4;

export function snapshotMaxAgeSeconds(): number {
  return snapshotTtlSeconds - Math.floor(Math.random() * snapshotJitterSeconds);
}

interface SnapshotCell {
  memo?: { at: number; snapshot: TagSnapshot | null };
  read?: Promise<TagSnapshot | null>;
}

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

export function dropSnapshotMemo(
  cfg: { isrPrefix: string },
  store: ObjectStoreReader,
): void {
  snapshotCells.get(store)?.delete(cfg.isrPrefix);
}

export async function invalidateSnapshot(
  cfg: { isrPrefix: string },
  deps: TagClockDeps,
): Promise<void> {
  dropSnapshotMemo(cfg, deps.store);
  try {
    const key = tagSnapshotKey(cfg.isrPrefix);
    await deps.snapshotCache?.delete?.(new Request(snapshotCacheUrl(key)));
  } catch {
  }
}

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

async function snapshotRecords(
  cfg: { isrPrefix: string },
  deps: TagClockDeps,
  now: number,
): Promise<Map<string, TagRecord> | null> {
  const snapshot = await readSnapshot(cfg, deps, now);
  return snapshot && new Map(Object.entries(snapshot.records));
}

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

function encodeKeyPath(key: string): string {
  return key.split("/").map(encodeURIComponent).join("/");
}

function snapshotCacheUrl(key: string): string {
  return `https://isr.ocel/${encodeKeyPath(key)}`;
}
