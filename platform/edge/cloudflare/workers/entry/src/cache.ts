import { enqueued, type RevalidationRoute, type RevalidationSender } from "./revalidation";
import type { TagClock, TagVerdict } from "./tag-clock";

const ENTRY_MODIFIED = "x-ocel-entry-modified";
const ENTRY_WINDOW = "x-ocel-entry-window";
const ENTRY_VERSION = "x-ocel-entry-version";
const ENTRY_FORMAT = "1";
const STATIC_WINDOW = 31536000;

export const CACHE_STATUS = "x-ocel-cache";
export const NEXT_CACHE_STATUS = "x-nextjs-cache";
export const VERCEL_CACHE_STATUS = "x-vercel-cache";
const DRAFT_COOKIE = "__prerender_bypass";

const SEGMENT_PREFETCH = "next-router-segment-prefetch";
const POSTPONED = "x-nextjs-postponed";
const SEGMENT_PAYLOAD = "2";

export const SUPPRESS_SELF_REVALIDATION = true;

export type CacheStatus = "HIT" | "PRERENDER" | "MISS" | "STALE" | "BYPASS";
type NextCacheStatus = "HIT" | "STALE";

const nextCacheStatus = (stale: boolean): NextCacheStatus =>
  stale ? "STALE" : "HIT";

export interface CacheDeps {
  cache: Cache;
  waitUntil: (promise: Promise<unknown>) => void;
  now?: () => number;
  admissionDelay?: (staleForMs: number) => Promise<void>;
  satisfiedFromBelow?: (refreshing: number) => Promise<boolean>;
  enqueueRevalidation?: RevalidationSender;
  joinFillTimeoutMs?: number;
}

export interface CacheTarget {
  key: string;
  refreshKey?: string;
  tags?: string[];
  revalidate?: number;
  expiration?: number;
  revalidation?: RevalidationRoute;
  segment?: boolean;
  suppressed?: boolean;
}

export interface CachePolicy {
  sMaxAge: number;
  swr: number;
}

function directives(cacheControl: string | null): Map<string, string> {
  const parsed = new Map<string, string>();
  if (!cacheControl) return parsed;
  for (const part of cacheControl.split(",")) {
    const [name, value = ""] = part.trim().toLowerCase().split("=");
    parsed.set(name, value);
  }
  return parsed;
}

export function deltaSeconds(
  cacheControl: string | null,
  ...names: string[]
): number | undefined {
  const parsed = directives(cacheControl);
  for (const name of names) {
    if (!parsed.has(name)) continue;
    const value = Number(parsed.get(name));
    if (Number.isFinite(value) && value >= 0) return value;
  }
  return undefined;
}

export function storagePolicy(cacheControl: string | null): CachePolicy | null {
  if (!cacheControl) return null;

  const parsed = directives(cacheControl);
  if (
    parsed.has("no-store") ||
    parsed.has("no-cache") ||
    parsed.has("private")
  ) {
    return null;
  }

  const sMaxAge = Number(parsed.get("s-maxage"));
  if (!Number.isFinite(sMaxAge) || sMaxAge <= 0) return null;

  const swr = Number(parsed.get("stale-while-revalidate") ?? 0);
  return { sMaxAge, swr: Number.isFinite(swr) && swr > 0 ? swr : 0 };
}

export interface EntryMeta {
  lastModified: number;
  revalidate?: number;
  expiration?: number;
}

export type Freshness = "fresh" | "stale" | "expired";

export function evaluate(meta: EntryMeta, now: number, tagStale: boolean): Freshness {
  const age = (now - meta.lastModified) / 1000;
  const timeStale = meta.revalidate !== undefined && age >= meta.revalidate;
  if (!timeStale && !tagStale) return "fresh";
  if (meta.expiration !== undefined && age >= meta.expiration) return "expired";
  return "stale";
}

export function staleWindowMs(meta: EntryMeta, now: number): number {
  if (meta.expiration === undefined) return Infinity;
  return meta.expiration * 1000 - (now - meta.lastModified);
}

export type CacheKeyResult =
  | { cacheable: true; key: string }
  | { cacheable: false };

export function variantPath(
  pathname: string,
  headers: Headers,
  renderingMode: "STATIC" | "PARTIALLY_STATIC" | undefined,
): string | null {
  if (headers.get("RSC") === null) return pathname; // HTML document / shell.

  const nextUrl = headers.get("next-url");
  const base = nextUrl !== null ? `${pathname}.iu/${encodeURIComponent(nextUrl)}` : pathname;

  const segment = headers.get("next-router-segment-prefetch");
  if (segment !== null) {
    return `${base}.segments/${encodeURIComponent(segment)}.segment.rsc`;
  }

  const prefetch = headers.get("next-router-prefetch");
  if (prefetch === "1") return `${base}.prefetch.rsc`;
  if (prefetch !== null) return null;

  return renderingMode === "STATIC" ? `${base}.rsc` : null;
}

export function isSegmentPrefetch(headers: Headers): boolean {
  return headers.get("RSC") !== null && headers.get(SEGMENT_PREFETCH) !== null;
}

export function isSegmentPayload(response: Response): boolean {
  return (
    response.status === 204 ||
    response.headers.get(POSTPONED) === SEGMENT_PAYLOAD
  );
}

export function asSegmentPayload(response: Response): Response {
  if (!response.ok || isSegmentPayload(response)) return response;
  response.body?.cancel();
  const headers = new Headers();
  const vary = response.headers.get("vary");
  if (vary) headers.set("vary", vary);
  return new Response(null, { status: 204, headers });
}

export function cacheKey(
  deploymentId: string,
  buildId: string,
  pathname: string,
  url: URL,
  headers: Headers,
  renderingMode: "STATIC" | "PARTIALLY_STATIC" | undefined,
  allowQuery?: string[],
): CacheKeyResult {
  const variant = variantPath(pathname, headers, renderingMode);
  if (variant === null) return { cacheable: false };

  const key = new URL(`https://cache.ocel/${deploymentId}/${buildId}${variant}`);
  const names = (allowQuery ?? [...url.searchParams.keys()]).filter(
    (name) => name !== "_rsc",
  );

  for (const name of [...names].sort()) {
    for (const value of url.searchParams.getAll(name)) {
      key.searchParams.append(name, value);
    }
  }

  return { cacheable: true, key: key.toString() };
}

export function hasDraftCookie(request: Request): boolean {
  const cookie = request.headers.get("cookie");
  return (
    cookie !== null && new RegExp(`(?:^|;\\s*)${DRAFT_COOKIE}=`).test(cookie)
  );
}

export function servedFromStore(response: Response, stale: boolean): Response {
  const served = withStatus(response, "PRERENDER");
  served.headers.set(NEXT_CACHE_STATUS, nextCacheStatus(stale));
  return served;
}

export function withStatus(response: Response, status: CacheStatus): Response {
  const headers = new Headers(response.headers);
  headers.set(CACHE_STATUS, status);
  return respond(response, headers);
}

export function withVercelCacheAlias(
  response: Response,
  enabled: boolean | undefined,
): Response {
  if (!enabled) return response;
  const status = response.headers.get(CACHE_STATUS);
  if (status === null) return response;
  const aliased = new Response(response.body, response);
  aliased.headers.set(VERCEL_CACHE_STATUS, status);
  return aliased;
}

function respond(response: Response, headers: Headers): Response {
  return new Response(response.body, {
    status: response.status,
    statusText: response.statusText,
    headers,
  });
}

function entryWindow(
  declared: string | null,
  target: CacheTarget,
): { revalidate?: number; expiration?: number } {
  const policy = storagePolicy(declared);
  if (!policy) {
    return { revalidate: target.revalidate, expiration: target.expiration };
  }
  return {
    revalidate: policy.sMaxAge,
    expiration: policy.swr > 0 ? policy.sMaxAge + policy.swr : target.expiration,
  };
}

interface ColoPolicy {
  storable(response: Response): boolean;
  window(
    declared: string | null,
    target: CacheTarget,
  ): { revalidate?: number; expiration?: number };
  forServe(response: Response, status: CacheStatus): Response;
}

function suppressedStaleServe(target: CacheTarget, response: Response): boolean {
  return (
    SUPPRESS_SELF_REVALIDATION &&
    target.suppressed === true &&
    response.headers.get(NEXT_CACHE_STATUS) === "STALE" &&
    response.headers.get(CACHE_STATUS) !== "PRERENDER"
  );
}

const prerenderPolicy: ColoPolicy = {
  storable: (response) =>
    storagePolicy(response.headers.get("cache-control")) !== null,
  window: entryWindow,
  forServe: (response, status) => {
    const served = withStatus(response, status);
    served.headers.set("cache-control", "public, max-age=0, must-revalidate");
    return served;
  },
};

export function imageStorable(response: Response): boolean {
  return (deltaSeconds(response.headers.get("cache-control"), "max-age") ?? 0) > 0;
}

function imagePolicy(servedCacheControl: string | undefined): ColoPolicy {
  return {
    storable: imageStorable,
    window: (declared, target) => ({
      revalidate: deltaSeconds(declared, "max-age"),
      expiration: target.expiration,
    }),
    forServe: (response, status) => {
      const served = withStatus(response, status);
      if (servedCacheControl && response.status === 200) {
        served.headers.set("cache-control", servedCacheControl);
      }
      return served;
    },
  };
}

function forStorage(
  response: Response,
  target: CacheTarget,
  policy: ColoPolicy,
  storedAt: number,
): Response {
  const headers = new Headers(response.headers);
  const modified = response.headers.get(ENTRY_MODIFIED) ?? String(storedAt);
  headers.set(ENTRY_MODIFIED, modified);
  headers.set(ENTRY_VERSION, ENTRY_FORMAT);
  const declared = response.headers.get("cache-control");
  if (declared) headers.set(ENTRY_WINDOW, declared);
  const { expiration } = policy.window(declared, target);
  headers.set("cache-control", `s-maxage=${expiration ?? STATIC_WINDOW}`);
  if (target.tags?.length) headers.set("cache-tag", target.tags.join(","));

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

export type StoredFrom = "serve" | "refresh";

async function store(
  keyRequest: Request,
  target: CacheTarget,
  deps: CacheDeps,
  policy: ColoPolicy,
  response: Response,
  from: StoredFrom,
): Promise<void> {
  if (response.status !== 200) {
    response.body?.cancel();
    return;
  }
  if (!policy.storable(response)) {
    response.body?.cancel();
    return;
  }
  if (from === "serve" && suppressedStaleServe(target, response)) {
    response.body?.cancel();
    return;
  }
  const now = deps.now ?? Date.now;
  await deps.cache.put(keyRequest, forStorage(response, target, policy, now()));
}

export async function storeInColo(
  target: CacheTarget,
  deps: CacheDeps,
  response: Response,
): Promise<void> {
  await store(new Request(target.key), target, deps, prerenderPolicy, response, "refresh");
}

const inFlight = new WeakMap<Cache, Map<string, Promise<unknown>>>();

export function refreshOnce(
  deps: CacheDeps,
  key: string,
  run: () => Promise<unknown>,
): void {
  let pending = inFlight.get(deps.cache);
  if (!pending) inFlight.set(deps.cache, (pending = new Map()));
  if (pending.has(key)) return;

  const promise = run()
    .catch(() => {})
    .finally(() => pending.delete(key));
  pending.set(key, promise);
  deps.waitUntil(promise);
}

function inFlightFill(
  deps: CacheDeps,
  key: string,
): Promise<unknown> | undefined {
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

export const refreshSentinelTtlSeconds = 5;

export const admissionJitterMs = 1_000;

export function admissionDrawMs(staleForMs: number): number {
  return Math.random() * Math.min(admissionJitterMs, Math.max(0, staleForMs));
}

const jitteredAdmissionDelay = (staleForMs: number): Promise<void> =>
  new Promise((done) => setTimeout(done, admissionDrawMs(staleForMs)));

export function sentinelUrl(key: string): string {
  return `https://refresh.ocel/${key.split("/").map(encodeURIComponent).join("/")}`;
}

function sentinelRecord(seconds: number): Response {
  return new Response(null, {
    headers: { "cache-control": `max-age=${seconds}` },
  });
}

async function claimSentinel(cache: Cache, sentinel: Request): Promise<boolean> {
  try {
    if (await cache.match(sentinel)) return false;
    await cache.put(sentinel, sentinelRecord(refreshSentinelTtlSeconds));
  } catch {
  }
  return true;
}

export type RefreshOutcome = "landed" | "failed" | "refused";

export function refreshOutcome(response: Response): RefreshOutcome {
  return response.ok && response.status !== 204 ? "landed" : "refused";
}

export const refreshBackoffSeconds = 30;

async function settleSentinel(
  cache: Cache,
  sentinel: Request,
  outcome: RefreshOutcome,
): Promise<void> {
  try {
    if (outcome === "failed") await cache.delete(sentinel);
    else {
      await cache.put(
        sentinel,
        sentinelRecord(
          outcome === "refused"
            ? refreshBackoffSeconds
            : refreshSentinelTtlSeconds,
        ),
      );
    }
  } catch {
  }
}

async function askBelow(deps: CacheDeps, refreshing: number): Promise<boolean> {
  try {
    return (await deps.satisfiedFromBelow?.(refreshing)) === true;
  } catch {
    return false;
  }
}

export function admitRefresh(
  deps: CacheDeps,
  key: string,
  refreshing: number,
  run: () => Promise<RefreshOutcome>,
  staleForMs = Infinity,
): void {
  const sentinel = new Request(sentinelUrl(key));
  refreshOnce(deps, key, async () => {
    await (deps.admissionDelay ?? jitteredAdmissionDelay)(staleForMs);
    if (!(await claimSentinel(deps.cache, sentinel))) return;
    let outcome: RefreshOutcome = "failed";
    try {
      outcome = (await askBelow(deps, refreshing)) ? "landed" : await run();
    } finally {
      await settleSentinel(deps.cache, sentinel, outcome);
    }
  });
}

async function serveOrAdmitRefresh(
  keyRequest: Request,
  target: CacheTarget,
  deps: CacheDeps,
  policy: ColoPolicy,
  originBlocking: () => Promise<Response>,
  tagClock?: TagClock,
): Promise<Response | null> {
  const now = deps.now ?? Date.now;
  const cached = await deps.cache.match(keyRequest);
  if (!cached || cached.headers.get(ENTRY_VERSION) !== ENTRY_FORMAT) return null;
  if (target.segment && !isSegmentPayload(cached)) {
    cached.body?.cancel();
    return null;
  }

  const modified = Number(cached.headers.get(ENTRY_MODIFIED));
  if (!Number.isFinite(modified)) return null;

  const tags = target.tags ?? [];
  let tagStale = false;
  if (tags.length > 0 && tagClock) {
    const verdict: TagVerdict = await tagClock.freshness(tags, modified, now());
    if (verdict === "expired") {
      cached.body?.cancel();
      return null;
    }
    tagStale = verdict !== "fresh";
  }
  const meta: EntryMeta = {
    lastModified: modified,
    ...policy.window(cached.headers.get(ENTRY_WINDOW), target),
  };
  const state = evaluate(meta, now(), tagStale);
  if (state === "fresh") {
    return policy.forServe(fromStorage(cached, false), "HIT");
  }
  if (state === "stale") {
    const refresh = async () => {
      if (await enqueued(deps.enqueueRevalidation, target.revalidation, modified)) {
        return "landed";
      }
      const response = await originBlocking();
      const outcome = refreshOutcome(response);
      await store(keyRequest, target, deps, policy, response, "refresh");
      return outcome;
    };
    if (target.refreshKey) {
      admitRefresh(
        deps,
        target.refreshKey,
        modified,
        refresh,
        staleWindowMs(meta, now()),
      );
    } else refreshOnce(deps, target.key, refresh);
    return policy.forServe(fromStorage(cached, true), "STALE");
  }
  return null;
}

async function colo(
  target: CacheTarget,
  deps: CacheDeps,
  policy: ColoPolicy,
  origin: () => Promise<Response>,
  originBlocking: () => Promise<Response>,
  tagClock?: TagClock,
): Promise<Response> {
  const keyRequest = new Request(target.key);
  const serveOrRefresh = () =>
    serveOrAdmitRefresh(keyRequest, target, deps, policy, originBlocking, tagClock);

  const hit = await serveOrRefresh();
  if (hit) return hit;

  const filling = inFlightFill(deps, target.key);
  if (filling) {
    const settled = await settledWithin(
      filling,
      deps.joinFillTimeoutMs ?? joinFillTimeoutMs,
    );
    if (settled) {
      const joined = await serveOrRefresh();
      if (joined) return joined;
    }
  }

  const pending = origin();
  refreshOnce(deps, target.key, () =>
    pending.then((response) =>
      store(keyRequest, target, deps, policy, response.clone(), "serve"),
    ),
  );
  const response = await pending;
  const status: CacheStatus =
    response.headers.get(CACHE_STATUS) === "PRERENDER" ? "PRERENDER" : "MISS";
  const result = policy.forServe(response, status);
  result.headers.delete(ENTRY_MODIFIED);
  return result;
}

export async function serveCached(
  request: Request,
  target: CacheTarget,
  deps: CacheDeps,
  origin: () => Promise<Response>,
  originBlocking: () => Promise<Response>,
  tagClock?: TagClock,
): Promise<Response> {
  if (request.method !== "GET" || hasDraftCookie(request)) {
    const result = withStatus(await origin(), "BYPASS");
    result.headers.delete(ENTRY_MODIFIED);
    return result;
  }

  return colo(target, deps, prerenderPolicy, origin, originBlocking, tagClock);
}

export function answerableImageRequest(request: Request): boolean {
  return request.method === "GET" || request.method === "HEAD";
}

export async function serveCachedImage(
  request: Request,
  target: CacheTarget,
  deps: CacheDeps | undefined,
  origin: () => Promise<Response>,
  originBlocking: () => Promise<Response>,
  servedCacheControl?: string,
): Promise<Response> {
  const policy = imagePolicy(servedCacheControl);
  const response =
    deps && answerableImageRequest(request)
      ? await colo(target, deps, policy, origin, originBlocking)
      : policy.forServe(await origin(), "BYPASS");

  return request.method === "HEAD" ? headResponse(response) : response;
}

export function headResponse(response: Response): Response {
  response.body?.cancel();
  return new Response(null, {
    status: response.status,
    statusText: response.statusText,
    headers: response.headers,
  });
}
