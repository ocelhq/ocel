// The edge cache logic for prerendered routes.
import type { TagClock, TagVerdict } from "./tag-clock";

const ENTRY_MODIFIED = "x-ocel-entry-modified";
// The freshness the tier below declared for this entry, kept verbatim beside it
// because the entry's own cache-control is rewritten to its retention window.
const ENTRY_WINDOW = "x-ocel-entry-window";
// The version of the entry format itself. An entry whose version is absent or
// unrecognized is a miss, never a misread: the durable tier PR 6 adds writes
// entries beside these, and a format change there bumps this constant instead
// of the cache key, so nothing has to be flushed and nothing is reinterpreted.
const ENTRY_VERSION = "x-ocel-entry-version";
const ENTRY_FORMAT = "1";
const STATIC_WINDOW = 31536000;

// The one status header every served route carries, reporting which tier
// answered and how fresh what it answered with was: HIT (this colo's cache),
// STALE (this colo's cache, past its window and being refreshed behind the
// request), PRERENDER (the R2 ISR store, one tier down), MISS (neither — the
// Lambda origin rendered it), or BYPASS.
export const CACHE_STATUS = "x-ocel-cache";
// Next's own header, reporting the freshness of the entry answering the
// request. Next stamps it inside the server, so a Lambda response arrives
// carrying it, and only for the routes Next stamps at all — a dynamic render
// never gets one. A cache tier answering without the Lambda has to restate it,
// because it, not the Lambda, knows the freshness of what it is about to serve.
export const NEXT_CACHE_STATUS = "x-nextjs-cache";
const DRAFT_COOKIE = "__prerender_bypass";

export type CacheStatus = "HIT" | "PRERENDER" | "MISS" | "STALE" | "BYPASS";
type NextCacheStatus = "HIT" | "STALE";

const nextCacheStatus = (stale: boolean): NextCacheStatus =>
  stale ? "STALE" : "HIT";

export interface CacheDeps {
  cache: Cache;
  waitUntil: (promise: Promise<unknown>) => void;
  // Injected so freshness never depends on wall-clock time. Milliseconds.
  now?: () => number;
}

export interface CacheTarget {
  key: string;
  tags?: string[];
  revalidate?: number;
  expiration?: number;
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

// The first of the named delta-seconds directives this cache-control actually
// carries. The order is the caller's: an upstream TTL reads s-maxage ahead of
// max-age, while an image entry's own window is only ever a max-age.
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

// The single stale-while-revalidate verdict, shared by the colo tier (`colo`,
// which both the prerender and the image route class reach) and the R2 tier
// (intercept). tagStale folds both a truly invalidated tag and an unreadable tag
// clock into "treat as stale"; the caller decides that policy per tier.
// "expired" means too old to serve even stale — the caller falls open to the
// tier below.
export function evaluate(meta: EntryMeta, now: number, tagStale: boolean): Freshness {
  const age = (now - meta.lastModified) / 1000;
  const timeStale = meta.revalidate !== undefined && age >= meta.revalidate;
  if (!timeStale && !tagStale) return "fresh";
  if (meta.expiration !== undefined && age >= meta.expiration) return "expired";
  return "stale";
}

export type CacheKeyResult =
  | { cacheable: true; key: string }
  | { cacheable: false };

// variantPath maps a request to the synthetic pathname that names its response
// variant — the closed, self-describing set of shapes a prerendered route can
// answer with. It returns null for a per-visitor dynamic variant, which must
// never enter a shared cache. This replaces Next's `_rsc` cache-buster: we own
// the Worker, so we key on the vary headers directly rather than on their hash.
export function variantPath(
  pathname: string,
  headers: Headers,
  renderingMode: "STATIC" | "PARTIALLY_STATIC" | undefined,
): string | null {
  if (headers.get("RSC") === null) return pathname; // HTML document / shell.

  // A segment prefetch names one prerendered segment; it is static build output
  // regardless of the prefetch value.
  const segment = headers.get("next-router-segment-prefetch");
  if (segment !== null) {
    return `${pathname}.segments/${encodeURIComponent(segment)}.segment.rsc`;
  }

  // Only next-router-prefetch: 1 is a static full-route prefetch; 2/3 are
  // runtime prefetches that intentionally perform a dynamic request.
  const prefetch = headers.get("next-router-prefetch");
  if (prefetch === "1") return `${pathname}.prefetch.rsc`;
  if (prefetch !== null) return null;

  // Bare RSC with no prefetch is a cacheable payload only on a fully static
  // route; on a PPR route it is the dynamic navigation that fills the holes.
  return renderingMode === "STATIC" ? `${pathname}.rsc` : null;
}

export function cacheKey(
  buildId: string,
  pathname: string,
  url: URL,
  headers: Headers,
  renderingMode: "STATIC" | "PARTIALLY_STATIC" | undefined,
  allowQuery?: string[],
): CacheKeyResult {
  const variant = variantPath(pathname, headers, renderingMode);
  if (variant === null) return { cacheable: false };

  const key = new URL(`https://cache.ocel/${buildId}${variant}`);
  // `_rsc` is Next's cache-buster for path-keyed CDNs; we key on the headers it
  // hashes instead, so it is always dropped from our key. The browser still
  // sends it and the origin still receives it — this only excludes it here.
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

// servedFromStore stamps both status headers on an entry the R2 ISR store
// answered without the Lambda: the Ocel tier that served it, and — since that
// entry never reached Next's server — the freshness Next itself would have
// reported for it.
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

function respond(response: Response, headers: Headers): Response {
  return new Response(response.body, {
    status: response.status,
    statusText: response.statusText,
    headers,
  });
}

// The window an entry is judged by. What the tier below declared on the
// response wins: it describes the render that actually happened, and for a path
// generated on demand it is the only window there is — the build manifest names
// only the paths it prerendered. A bare s-maxage (what the R2 tier synthesizes)
// declares a revalidate window and nothing about expiry, so how long the entry
// may still be served stale stays the manifest's to say.
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

// Everything the two route classes sharing this tier disagree about. The
// sequence they agree on — look the entry up, judge it, serve it or fall
// through — is `colo`, and exists once.
//
// A prerendered route's response declares an s-maxage window the browser must
// never see (it revalidates on every navigation) and its entry is judged by
// that window. An optimized image's response declares the browser's own window,
// derived from the upstream image server's, and is served with it intact — so
// neither the storability gate nor the served headers can be shared.
interface ColoPolicy {
  storable(response: Response): boolean;
  window(
    declared: string | null,
    target: CacheTarget,
  ): { revalidate?: number; expiration?: number };
  forServe(response: Response, status: CacheStatus): Response;
}

const prerenderPolicy: ColoPolicy = {
  // Only a storability gate: refuse no-store/private/no s-maxage responses. The
  // retention window itself comes from the target.
  storable: (response) =>
    storagePolicy(response.headers.get("cache-control")) !== null,
  window: entryWindow,
  forServe: (response, status) => {
    const served = withStatus(response, status);
    // The entry's own s-maxage window is this tier's bookkeeping and must never
    // reach the browser, which revalidates on every navigation instead — so the
    // header the client is left holding is written here, not carried over.
    served.headers.set("cache-control", "public, max-age=0, must-revalidate");
    return served;
  },
};

// Built per request, because what the browser is told depends on the request and
// not on the entry: identical bytes reached through a content-hashed static
// import url and through its `public/` twin share one entry, and only the former
// may be called immutable. A module-level policy would leak one request's answer
// into another's, in an isolate serving many at once.
function imagePolicy(servedCacheControl: string | undefined): ColoPolicy {
  return {
    // An image entry carries the freshness window as a plain max-age, so there
    // is no separate storability signal to read: a max-age of zero is the whole
    // of what "do not keep this" can look like here.
    storable: (response) =>
      (deltaSeconds(response.headers.get("cache-control"), "max-age") ?? 0) > 0,
    // No expiration, and deliberately so. Next encodes an expireAt in the
    // entry's filename, but ImageOptimizerCache.get uses it for one thing only —
    // `isStale: now > expireAt` — and returns the buffer whatever it says, with
    // `expire: undefined` so the response cache has no hard cutoff to enforce
    // either. An image is only ever evicted by the disk LRU, never by age.
    // Nothing here may return "expired": the tier below holds the same optimized
    // bytes under the same key, so falling through to it would trade a served
    // image for a blocking optimization and gain nothing. PR 6's R2 tier
    // inherits that — it is reached on a miss, not on an aged-out entry.
    window: (declared, target) => ({
      revalidate: deltaSeconds(declared, "max-age"),
      expiration: target.expiration,
    }),
    forServe: (response, status) => {
      const served = withStatus(response, status);
      // Absent, the derived window is also what the browser gets. Present, it
      // is a claim about the url this request used — never about the entry —
      // and so it is applied here rather than stored.
      //
      // A 200 only. The claim is about bytes that were optimized, and a
      // static-import url's claim is `immutable` for ten years: stamped on the
      // 502 a substrate with no optimizer answers, or on the 403 a rotated edge
      // key answers, it would pin that failure in every browser that saw it,
      // unrevalidatable, past any redeploy, re-bootstrap or purge. The colo tier
      // refuses to store a non-200 for exactly this reason and cannot be told
      // about a browser's. Next's own errors carry no Cache-Control at all, which
      // is also what the conformance fixtures record, so bare is both safe (a 4xx
      // or 5xx here is not heuristically cacheable) and the conforming answer.
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
  // Physical retention is decoupled from logical freshness: keep the object as
  // long as it could ever be served (through its expiration window, or a year
  // for a static entry), and let evaluate decide fresh/stale/expired per hit.
  const { expiration } = policy.window(declared, target);
  headers.set("cache-control", `s-maxage=${expiration ?? STATIC_WINDOW}`);
  if (target.tags?.length) headers.set("cache-tag", target.tags.join(","));

  return respond(response, headers);
}

// The stored entry as the response the tier below declared: its own bookkeeping
// dropped and its cache-control restored from the copy kept beside it, leaving
// the policy to decide what of that the browser is told.
function fromStorage(response: Response, stale: boolean): Response {
  const headers = new Headers(response.headers);
  const declared = headers.get(ENTRY_WINDOW);
  if (declared) headers.set("cache-control", declared);
  else headers.delete("cache-control");
  headers.delete(ENTRY_MODIFIED);
  headers.delete(ENTRY_WINDOW);
  headers.delete(ENTRY_VERSION);
  headers.delete("cache-tag");
  // The stored value dates from the write, not this serve; restate it — but
  // only where there is one, so a dynamic response stays unstamped.
  if (headers.has(NEXT_CACHE_STATUS)) {
    headers.set(NEXT_CACHE_STATUS, nextCacheStatus(stale));
  }

  return respond(response, headers);
}

async function store(
  keyRequest: Request,
  target: CacheTarget,
  deps: CacheDeps,
  policy: ColoPolicy,
  response: Response,
): Promise<void> {
  if (response.status !== 200) {
    response.body?.cancel();
    return;
  }
  if (!policy.storable(response)) {
    response.body?.cancel();
    return;
  }
  const now = deps.now ?? Date.now;
  await deps.cache.put(keyRequest, forStorage(response, target, policy, now()));
}

// storeInColo lets a background refresh (which owns the fresh Lambda response)
// write it straight into the colo cache. The R2 write is the Lambda's own side
// effect; the worker only ever writes colo.
export async function storeInColo(
  target: CacheTarget,
  deps: CacheDeps,
  response: Response,
): Promise<void> {
  await store(new Request(target.key), target, deps, prerenderPolicy, response);
}

// Every request arriving on a stale entry would otherwise start its own origin
// render, so one isolate can put a burst of identical regenerations on a single
// Lambda. Keyed by the cache object, which is one stable instance per isolate,
// so the in-flight set neither leaks between isolates nor between tests.
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

// The colo tier itself: lookup, verdict, serve-or-refresh. Both route classes
// run exactly this, and differ only in the policy they bring.
async function colo(
  target: CacheTarget,
  deps: CacheDeps,
  policy: ColoPolicy,
  origin: () => Promise<Response>,
  originBlocking: () => Promise<Response>,
  tagClock?: TagClock,
): Promise<Response> {
  const keyRequest = new Request(target.key);
  const now = deps.now ?? Date.now;
  const cached = await deps.cache.match(keyRequest);

  if (cached && cached.headers.get(ENTRY_VERSION) === ENTRY_FORMAT) {
    const modified = Number(cached.headers.get(ENTRY_MODIFIED));
    if (Number.isFinite(modified)) {
      const tags = target.tags ?? [];
      let tagStale = false;
      if (tags.length > 0 && tagClock) {
        const verdict: TagVerdict = await tagClock.expired(tags, modified, now());
        // Colo tier: an invalidated tag AND an untrusted snapshot both serve
        // stale-while-revalidate — we already hold the content, and the refresh
        // drives the Lambda to republish the tag clock. (intercept, one tier
        // down, falls open on "untrusted" instead.)
        tagStale = verdict !== false;
      }
      const state = evaluate(
        {
          lastModified: modified,
          ...policy.window(cached.headers.get(ENTRY_WINDOW), target),
        },
        now(),
        tagStale,
      );
      if (state === "fresh") {
        return policy.forServe(fromStorage(cached, false), "HIT");
      }
      if (state === "stale") {
        // Serving stale is what triggers the background refresh, which forces a
        // blocking origin render so the entry is rewritten fresh for the next
        // request. The serve itself does not wait on it.
        refreshOnce(deps, target.key, () =>
          originBlocking().then((response) =>
            store(keyRequest, target, deps, policy, response),
          ),
        );
        return policy.forServe(fromStorage(cached, true), "STALE");
      }
      // "expired": fall through — R2 may already hold a fresher entry.
    }
  }

  const response = await origin();
  const served: CacheStatus =
    response.headers.get(CACHE_STATUS) === "PRERENDER" ? "PRERENDER" : "MISS";
  // Single-writer populate: a burst of concurrent misses collapses to one put.
  // refreshOnce runs the thunk synchronously (so the clone precedes forServe
  // consuming the body) and dedups by key; a deduped caller never clones.
  refreshOnce(deps, target.key, () =>
    store(keyRequest, target, deps, policy, response.clone()),
  );
  const result = policy.forServe(response, served);
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

// The image tier. There is no separate blocking origin to refresh from — an
// optimization has no draft or bypass mode, so the one call is both — and no
// tag clock: an image is invalidated by its content hash changing, which is a
// different key rather than a stale entry.
//
// servedCacheControl overrides the header the browser is given without touching
// the entry; see imagePolicy.
export async function serveCachedImage(
  request: Request,
  target: CacheTarget,
  deps: CacheDeps | undefined,
  origin: () => Promise<Response>,
  servedCacheControl?: string,
): Promise<Response> {
  const policy = imagePolicy(servedCacheControl);
  // HEAD is safe and cacheable, and an optimization has none of the per-visitor
  // semantics that make a prerender bypass on it, so it is answered from the
  // entry a GET would read and populates that entry in full — the lookup key is
  // synthesized, so it is a GET whatever the method was. Only the body is
  // dropped, once, at the end.
  const answerable = request.method === "GET" || request.method === "HEAD";
  // No cache to consult (a routing test, or a substrate that bound none) and a
  // method no cache may answer are the same case: served, and honest about
  // having been served uncached.
  const response =
    deps && answerable
      ? await colo(target, deps, policy, origin, origin)
      : policy.forServe(await origin(), "BYPASS");

  return request.method === "HEAD" ? headResponse(response) : response;
}

// A HEAD carries the headers of the GET it stands for and none of its body.
function headResponse(response: Response): Response {
  response.body?.cancel();
  return new Response(null, {
    status: response.status,
    statusText: response.statusText,
    headers: response.headers,
  });
}
