import {
  CACHE_STATUS,
  type CacheStatus,
  deltaSeconds,
  headResponse,
  NEXT_CACHE_STATUS,
  respond,
  withStatus,
} from "@framework/next-router/http-cache";

const ENTRY_MODIFIED = "x-ocel-entry-modified";
const ENTRY_WINDOW = "x-ocel-entry-window";
const ENTRY_VERSION = "x-ocel-entry-version";
const ENTRY_FORMAT = "1";
const STATIC_WINDOW = 31536000;

type NextCacheStatus = "HIT" | "STALE";

const nextCacheStatus = (stale: boolean): NextCacheStatus => (stale ? "STALE" : "HIT");

export interface CacheDeps {
  cache: Cache;
  waitUntil: (promise: Promise<unknown>) => void;
  now?: () => number;
  joinFillTimeoutMs?: number;
}

export interface CacheTarget {
  key: string;
}

export type OriginBlocking = (refreshing: number) => Promise<Response>;

export interface EntryMeta {
  lastModified: number;
  revalidate?: number;
}

export type Freshness = "fresh" | "stale";

export function evaluate(meta: EntryMeta, now: number): Freshness {
  const age = (now - meta.lastModified) / 1000;
  return meta.revalidate !== undefined && age >= meta.revalidate ? "stale" : "fresh";
}

export function servedFromStore(response: Response, stale: boolean): Response {
  const served = withStatus(response, "PRERENDER");
  served.headers.set(NEXT_CACHE_STATUS, nextCacheStatus(stale));
  return served;
}

interface ColoPolicy {
  storable(response: Response): boolean;
  window(declared: string | null): { revalidate?: number };
  forServe(response: Response, status: CacheStatus): Response;
}

export function imageStorable(response: Response): boolean {
  return (deltaSeconds(response.headers.get("cache-control"), "max-age") ?? 0) > 0;
}

function imagePolicy(servedCacheControl: string | undefined): ColoPolicy {
  return {
    storable: imageStorable,
    window: (declared) => ({ revalidate: deltaSeconds(declared, "max-age") }),
    forServe: (response, status) => {
      const served = withStatus(response, status);
      if (servedCacheControl && response.status === 200) {
        served.headers.set("cache-control", servedCacheControl);
      }
      return served;
    },
  };
}

function forStorage(response: Response, storedAt: number): Response {
  const headers = new Headers(response.headers);
  const modified = response.headers.get(ENTRY_MODIFIED) ?? String(storedAt);
  headers.set(ENTRY_MODIFIED, modified);
  headers.set(ENTRY_VERSION, ENTRY_FORMAT);
  const declared = response.headers.get("cache-control");
  if (declared) headers.set(ENTRY_WINDOW, declared);
  headers.set("cache-control", `s-maxage=${STATIC_WINDOW}`);

  return respond(response, headers);
}

function fromStorage(response: Response, stale: boolean): Response {
  const headers = new Headers(response.headers);
  const declared = headers.get(ENTRY_WINDOW);
  if (declared) headers.set("cache-control", declared);
  else headers.delete("cache-control");
  headers.delete(ENTRY_MODIFIED);
  headers.delete(ENTRY_WINDOW);
  headers.delete(ENTRY_VERSION);
  headers.delete("cache-tag");
  if (headers.has(NEXT_CACHE_STATUS)) {
    headers.set(NEXT_CACHE_STATUS, nextCacheStatus(stale));
  }

  return respond(response, headers);
}

async function store(
  keyRequest: Request,
  deps: CacheDeps,
  policy: ColoPolicy,
  response: Response,
): Promise<void> {
  if (response.status !== 200 || !policy.storable(response)) {
    response.body?.cancel();
    return;
  }
  const now = deps.now ?? Date.now;
  await deps.cache.put(keyRequest, forStorage(response, now()));
}

const inFlight = new WeakMap<Cache, Map<string, Promise<unknown>>>();

export function refreshOnce(deps: CacheDeps, key: string, run: () => Promise<unknown>): void {
  let pending = inFlight.get(deps.cache);
  if (!pending) inFlight.set(deps.cache, (pending = new Map()));
  if (pending.has(key)) return;

  const promise = run()
    .catch(() => {})
    .finally(() => pending.delete(key));
  pending.set(key, promise);
  deps.waitUntil(promise);
}

function inFlightFill(deps: CacheDeps, key: string): Promise<unknown> | undefined {
  return inFlight.get(deps.cache)?.get(key);
}

export const joinFillTimeoutMs = 2_000;

export function settledWithin(promise: Promise<unknown>, ms: number): Promise<boolean> {
  return new Promise((resolve) => {
    const timer = setTimeout(() => resolve(false), ms);
    promise.then(
      () => {
        clearTimeout(timer);
        resolve(true);
      },
      () => {
        clearTimeout(timer);
        resolve(true);
      },
    );
  });
}

async function serveOrRefresh(
  keyRequest: Request,
  target: CacheTarget,
  deps: CacheDeps,
  policy: ColoPolicy,
  originBlocking: OriginBlocking,
): Promise<Response | null> {
  const now = deps.now ?? Date.now;
  const cached = await deps.cache.match(keyRequest);
  if (!cached || cached.headers.get(ENTRY_VERSION) !== ENTRY_FORMAT) return null;

  const modified = Number(cached.headers.get(ENTRY_MODIFIED));
  if (!Number.isFinite(modified)) return null;

  const meta: EntryMeta = {
    lastModified: modified,
    ...policy.window(cached.headers.get(ENTRY_WINDOW)),
  };
  const state = evaluate(meta, now());
  if (state === "fresh") {
    return policy.forServe(fromStorage(cached, false), "HIT");
  }
  refreshOnce(deps, target.key, async () => {
    await store(keyRequest, deps, policy, await originBlocking(modified));
  });
  return policy.forServe(fromStorage(cached, true), "STALE");
}

async function colo(
  target: CacheTarget,
  deps: CacheDeps,
  policy: ColoPolicy,
  origin: () => Promise<Response>,
  originBlocking: OriginBlocking,
): Promise<Response> {
  const keyRequest = new Request(target.key);
  const lookup = () => serveOrRefresh(keyRequest, target, deps, policy, originBlocking);

  const hit = await lookup();
  if (hit) return hit;

  const filling = inFlightFill(deps, target.key);
  if (filling) {
    const finished = await settledWithin(filling, deps.joinFillTimeoutMs ?? joinFillTimeoutMs);
    if (finished) {
      const joined = await lookup();
      if (joined) return joined;
    }
  }

  const pending = origin();
  refreshOnce(deps, target.key, () =>
    pending.then((response) => store(keyRequest, deps, policy, response.clone())),
  );
  const response = await pending;
  const status: CacheStatus =
    response.headers.get(CACHE_STATUS) === "PRERENDER" ? "PRERENDER" : "MISS";
  const result = policy.forServe(response, status);
  result.headers.delete(ENTRY_MODIFIED);
  return result;
}

export function answerableImageRequest(request: Request): boolean {
  return request.method === "GET" || request.method === "HEAD";
}

export async function serveCachedImage(
  request: Request,
  target: CacheTarget,
  deps: CacheDeps | undefined,
  origin: () => Promise<Response>,
  originBlocking: OriginBlocking,
  servedCacheControl?: string,
): Promise<Response> {
  const policy = imagePolicy(servedCacheControl);
  const response =
    deps && answerableImageRequest(request)
      ? await colo(target, deps, policy, origin, originBlocking)
      : policy.forServe(await origin(), "BYPASS");

  return request.method === "HEAD" ? headResponse(response) : response;
}
