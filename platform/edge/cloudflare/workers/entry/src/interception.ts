import {
  cacheKey,
  deserialize,
  tagsOf,
  type CacheEntryFile,
} from "@framework/next-cache";

import { evaluate, staleWindowMs } from "./cache";
import { lruSet } from "./lru";
import {
  createTagClock,
  parseJson,
  storeText,
  type ObjectStoreReader,
  type SnapshotCache,
  type TagClock,
} from "./tag-clock";

export interface InterceptionConfig {
  isrPrefix: string;
}

export interface InterceptTarget {
  routePath: string;
  revalidate: number | false | undefined;
  expiration?: number;
  fallbackPath?: string;
  tags?: string[];
}

export type Interception =
  | {
      kind: "complete";
      response: Response;
      stale: boolean;
      staleForMs?: number;
      lastModified: number;
    }
  | {
      kind: "ppr";
      shell: Response;
      postponed: string;
      stale: boolean;
      staleForMs?: number;
      lastModified: number;
    };

export interface InterceptDeps {
  store: ObjectStoreReader;
  snapshotCache?: SnapshotCache;
  now?: () => number;
  waitUntil?: (promise: Promise<unknown>) => void;
  tagClock?: TagClock;
  freshRead?: boolean;
}

const STATIC_WINDOW = 31536000;

const entryMemoTtlMs = 5_000;
const entryMemoMax = 256;

const entryMemo = new WeakMap<
  ObjectStoreReader,
  Map<string, { at: number; entry: CacheEntryFile }>
>();
const entryRefreshing = new WeakMap<ObjectStoreReader, Set<string>>();

export async function intercept(
  request: Request,
  target: InterceptTarget,
  cfg: InterceptionConfig,
  deps: InterceptDeps,
): Promise<Interception | null> {
  try {
    const now = (deps.now ?? Date.now)();
    const clock = deps.tagClock ?? createTagClock(cfg, deps);

    const segmentPath = request.headers.get("next-router-segment-prefetch");
    const entryP = readEntry(cfg, deps, target.routePath).then((concrete) =>
      concrete && (segmentPath === null || hasSegment(concrete.value, segmentPath))
        ? concrete
        : readFallbackEntry(cfg, deps, target, segmentPath).then(
            (fallback) => fallback ?? concrete,
          ),
    );
    const primeP = target.tags?.length ? clock.prime(now) : undefined;
    if (primeP) deps.waitUntil?.(primeP.catch(() => {}) as Promise<unknown>);

    const entry = await entryP;
    if (!entry) return null;

    const value = entry.value;
    if (!isServable(value)) return null;

    const ageSeconds = (now - entry.lastModified) / 1000;
    const { revalidate, expiration } = entryWindow(entry, target);
    const window =
      revalidate !== undefined
        ? Math.max(1, revalidate - Math.floor(ageSeconds))
        : STATIC_WINDOW;
    const meta = { lastModified: entry.lastModified, revalidate, expiration };

    const ungatedStaleness = (): { stale: boolean; staleForMs?: number } =>
      evaluate(meta, now, false) === "fresh"
        ? { stale: false }
        : { stale: true, staleForMs: Math.max(0, staleWindowMs(meta, now)) };

    if (segmentPath !== null && value.kind === "APP_PAGE") {
      const response = reconstructSegment(value, segmentPath);
      if (!response) return null;
      response.headers.set("x-nextjs-postponed", "2");
      response.headers.set("cache-control", `s-maxage=${window}`);
      response.headers.set("x-ocel-entry-modified", String(entry.lastModified));
      return {
        kind: "complete",
        response,
        lastModified: entry.lastModified,
        ...ungatedStaleness(),
      };
    }

    const prefetchMode = request.headers.get("next-router-prefetch");
    const isPrefetch = prefetchMode === "1";
    if (
      isPrefetch &&
      value.kind === "APP_PAGE" &&
      value.postponed !== undefined
    ) {
      const response = reconstruct(request, value);
      if (!response) return null;
      if (!response.headers.has("x-nextjs-postponed")) {
        response.headers.set("x-nextjs-postponed", "1");
      }
      response.headers.set("cache-control", `s-maxage=${window}`);
      response.headers.set("x-ocel-entry-modified", String(entry.lastModified));
      return {
        kind: "complete",
        response,
        lastModified: entry.lastModified,
        ...ungatedStaleness(),
      };
    }

    if (prefetchMode === "2" || prefetchMode === "3") {
      return null;
    }

    let tagStale = false;
    const tags = tagsOf(value, {});
    if (tags.length > 0) {
      const verdict = await clock.expired(tags, entry.lastModified, now);
      if (verdict === "untrusted") return null; // R2 tier: unknown never serves.
      tagStale = verdict;
    }

    const verdict = evaluate(meta, now, tagStale);
    if (verdict === "expired") return null;
    const isStale = verdict === "stale";
    const staleForMs = isStale ? staleWindowMs(meta, now) : undefined;

    if (value.kind === "APP_PAGE" && value.postponed !== undefined) {
      const shell = reconstruct(request, value);
      return (
        shell && {
          kind: "ppr",
          shell,
          postponed: value.postponed,
          stale: isStale,
          staleForMs,
          lastModified: entry.lastModified,
        }
      );
    }

    const response = reconstruct(request, value);
    if (!response) return null;
    response.headers.set("x-ocel-entry-modified", String(entry.lastModified));
    response.headers.set("cache-control", `s-maxage=${isStale ? 1 : window}`);
    return {
      kind: "complete",
      response,
      stale: isStale,
      staleForMs,
      lastModified: entry.lastModified,
    };
  } catch {
    return null;
  }
}

function entryWindow(
  entry: CacheEntryFile,
  target: InterceptTarget,
): { revalidate?: number; expiration?: number } {
  const declared = entry.cacheControl;
  if (declared === undefined) {
    return {
      revalidate:
        typeof target.revalidate === "number" ? target.revalidate : undefined,
      expiration: target.expiration,
    };
  }
  return {
    revalidate:
      typeof declared.revalidate === "number" ? declared.revalidate : undefined,
    expiration: declared.expire ?? target.expiration,
  };
}

function isServable(value: Record<string, any>): boolean {
  switch (value?.kind) {
    case "APP_PAGE":
    case "PAGES":
    case "APP_ROUTE":
      return true;
    default:
      return false;
  }
}

async function readFallbackEntry(
  cfg: InterceptionConfig,
  deps: InterceptDeps,
  target: InterceptTarget,
  segmentPath: string | null,
): Promise<CacheEntryFile | null> {
  if (!target.fallbackPath || target.fallbackPath === target.routePath) {
    return null;
  }
  const entry = await readEntry(cfg, deps, target.fallbackPath);
  if (!entry) return null;
  if (segmentPath !== null) {
    return hasSegment(entry.value, segmentPath) ? entry : null;
  }
  return entry.value?.postponed === undefined ? null : entry;
}

async function readEntry(
  cfg: InterceptionConfig,
  deps: InterceptDeps,
  routePath: string,
): Promise<CacheEntryFile | null> {
  const key = `${cfg.isrPrefix}/cache/${cacheKey(routePath)}.cache.json`;
  const now = (deps.now ?? Date.now)();
  const memo = entryMap(deps.store);

  const hit = deps.freshRead ? undefined : memo.get(key);
  if (hit) {
    if (now - hit.at < entryMemoTtlMs) return hit.entry;
    if (deps.waitUntil) {
      refreshEntry(deps, key);
      return hit.entry;
    }
    const fresh = await fetchEntry(deps.store, key);
    if (fresh) lruSet(memo, key, { at: now, entry: fresh }, entryMemoMax);
    else memo.delete(key);
    return fresh ?? null;
  }

  const entry = await fetchEntry(deps.store, key);
  if (entry) lruSet(memo, key, { at: now, entry }, entryMemoMax);
  return entry;
}

function entryMap(
  store: ObjectStoreReader,
): Map<string, { at: number; entry: CacheEntryFile }> {
  let map = entryMemo.get(store);
  if (!map) entryMemo.set(store, (map = new Map()));
  return map;
}

function refreshEntry(deps: InterceptDeps, key: string): void {
  let pending = entryRefreshing.get(deps.store);
  if (!pending) entryRefreshing.set(deps.store, (pending = new Set()));
  if (pending.has(key)) return;
  pending.add(key);

  const run = new Promise<void>((resolve) => setTimeout(resolve, 0))
    .then(async () => {
      const entry = await fetchEntry(deps.store, key);
      const map = entryMap(deps.store);
      if (entry) lruSet(map, key, { at: (deps.now ?? Date.now)(), entry }, entryMemoMax);
      else map.delete(key);
    })
    .catch(() => {})
    .finally(() => pending.delete(key));

  deps.waitUntil?.(run);
}

async function fetchEntry(
  store: ObjectStoreReader,
  key: string,
): Promise<CacheEntryFile | null> {
  const body = await storeText(store, key);
  if (body === null) return null;

  const entry = parseJson<CacheEntryFile>(body);
  if (!entry || typeof entry.lastModified !== "number" || !entry.value) {
    return null;
  }
  return entry;
}

function headersFrom(map: Record<string, any> | undefined): Headers {
  const headers = new Headers();
  for (const [name, v] of Object.entries(map ?? {})) {
    if (name.toLowerCase() === "x-next-cache-tags") continue;
    headers.set(name, String(v));
  }
  return headers;
}

function reconstruct(
  request: Request,
  value: Record<string, any>,
): Response | null {
  const restored = deserialize(value);
  const status = typeof value.status === "number" ? value.status : 200;

  let body: BodyInit;
  let headers: Headers;
  if (value.kind === "APP_ROUTE") {
    headers = headersFrom(value.headers);
    body = restored.body ?? new Uint8Array();
  } else if (value.kind === "APP_PAGE") {
    if (request.headers.get("RSC") === "1") {
      if (!restored.rscData) return null; // Negotiated RSC but the entry has none.
      body = restored.rscData;
      headers = headersFrom(value.rscHeaders);
      if (!headers.has("content-type")) {
        headers.set("content-type", "text/x-component");
      }
    } else {
      body = value.html ?? "";
      headers = headersFrom(value.headers);
      if (!headers.has("content-type")) {
        headers.set("content-type", "text/html; charset=utf-8");
      }
    }
  } else {
    headers = headersFrom(value.headers);
    body = value.html ?? "";
    if (!headers.has("content-type")) {
      headers.set("content-type", "text/html; charset=utf-8");
    }
  }

  return new Response(body, { status, headers });
}

function hasSegment(value: Record<string, any>, segmentPath: string): boolean {
  return (
    value?.segmentHeaders !== undefined &&
    value?.segmentData?.[segmentPath] !== undefined
  );
}

function reconstructSegment(
  value: Record<string, any>,
  segmentPath: string,
): Response | null {
  const segments: Map<string, Uint8Array> | undefined =
    deserialize(value).segmentData;
  const body = segments?.get(segmentPath);
  if (!body) return null;
  if (!value.segmentHeaders) return null;

  const headers = headersFrom(value.segmentHeaders);
  return new Response(body, { status: 200, headers });
}
