// Cache interception: the worker reads the authoritative ISR cache itself, so a
// cache hit skips the Lambda origin entirely. It mirrors OcelCacheHandler.get()
// using the shared @ocel/next-cache primitives, so the edge and the Lambda can
// never disagree about whether an entry is still servable.
//
// Entries come from the bound R2 cache store, and tag state from the tag-clock
// snapshot sitting beside them, so an interception reads exactly one store and
// never an AWS API.
//
// Interception is strictly additive: every miss, incomplete entry, untrusted
// tag snapshot, past-expiration entry, or error returns null so the caller falls
// open to the existing Lambda path. A stale-but-servable entry (a lapsed
// revalidate window or an invalidated tag, still inside expiration) is served
// stale-while-revalidate, marked stale so the caller refreshes it behind the
// request. A bug here can only ever cost the interception shortcut or serve one
// extra stale response, never a wrong one.
import {
  cacheKey,
  deserialize,
  tagsOf,
  type CacheEntryFile,
} from "@ocel/next-cache";

import {
  createTagClock,
  parseJson,
  storeText,
  type ObjectStoreReader,
  type SnapshotCache,
  type StoredObject,
  type TagClock,
} from "./tag-clock";

// The one coordinate the store read needs: the build prefix the entry and
// snapshot keys are rooted at. Interception is gated on the cache-store binding
// plus this prefix; absent either, the worker forwards as before.
export interface InterceptionConfig {
  prefix: string;
}

// The prerender facts interception needs: the concrete pathname keying the store
// entry, and the route's revalidate window (Next's Revalidate: seconds, or false
// for a static entry with no time-based expiry).
export interface InterceptTarget {
  routePath: string;
  revalidate: number | false | undefined;
  // How long a stale entry may still be served while a refresh runs behind it.
  // Only a PPR entry serves stale; absent, it never expires on time alone.
  expiration?: number;
  // The route's dynamic dispatch pattern (e.g. /posts/[id]). A concrete path
  // with no entry of its own falls back to this route's param-agnostic shell.
  fallbackPath?: string;
}

// What a read of the ISR cache produced. A complete entry answers the request on
// its own; a PPR entry answers only its static half, and its shell has to be
// composed with a resumed render before it is a response.
export type Interception =
  | { kind: "complete"; response: Response; stale: boolean }
  | { kind: "ppr"; shell: Response; postponed: string; stale: boolean };

export interface InterceptDeps {
  // The bound cache store entries and tag state are read from (the Cloudflare R2
  // binding in production, a fake in tests).
  store: ObjectStoreReader;
  // The PoP cache fronting the snapshot read. Absent, or inert as it is on
  // *.workers.dev, costs one store read per interception and nothing else.
  snapshotCache?: SnapshotCache;
  // Injected so freshness never depends on wall-clock time. Milliseconds.
  now?: () => number;
  // Schedules the background R2 refresh of a stale in-memory entry so the R2
  // round-trip never blocks the request that noticed the staleness. Absent
  // (routing tests) degrades to a blocking re-read on the next request.
  waitUntil?: (promise: Promise<unknown>) => void;
  // Overrides the tag clock built from the deps above. Absent, one is built
  // per call from `store`/`snapshotCache`/`waitUntil` via createTagClock.
  tagClock?: TagClock;
}

// A static entry (revalidate false/undefined) has no time-based expiry, only
// tag-based; it is memoized for a year, matching Next's own fully-static TTL.
const STATIC_WINDOW = 31536000;

// readEntry is the one R2 round-trip on the interception path, and a page's
// client segment cache fires a burst of prefetches at the same route entry
// within a colo. This memo, keyed by the store binding exactly like
// snapshotMemo, collapses that burst to one read and then serves every variant
// of a hot route from memory. Freshness and tag state are still evaluated per
// request against the entry's real lastModified and the snapshot, so the memo
// only freezes the entry bytes for one TTL — the same SWR contract serveCached
// uses one tier up.
const entryMemoTtlMs = 5_000;
const entryMemoMax = 256;

const entryMemo = new WeakMap<
  ObjectStoreReader,
  Map<string, { at: number; entry: CacheEntryFile }>
>();
const entryRefreshing = new WeakMap<ObjectStoreReader, Set<string>>();

// intercept attempts to answer a prerender target from the ISR cache. It returns
// a complete response, a PPR shell awaiting a resumed render, or null to fail
// open to the Lambda origin. It never throws.
export async function intercept(
  request: Request,
  target: InterceptTarget,
  cfg: InterceptionConfig,
  deps: InterceptDeps,
): Promise<Interception | null> {
  try {
    const now = (deps.now ?? Date.now)();

    const entry =
      (await readEntry(cfg, deps, target.routePath)) ??
      (await readFallbackShell(cfg, deps, target));
    if (!entry) return null;

    const value = entry.value;
    if (!isServable(value)) return null;

    const ageSeconds = (now - entry.lastModified) / 1000;
    const revalidate =
      typeof target.revalidate === "number" ? target.revalidate : undefined;
    const stale = revalidate !== undefined && ageSeconds >= revalidate;
    const window =
      revalidate !== undefined
        ? Math.max(1, revalidate - Math.floor(ageSeconds))
        : STATIC_WINDOW;

    // Prefetches are answered before the tag check, and independently of it. A
    // prefetch is speculative: its result is revealed only on a later
    // navigation, which always resumes the dynamic (tagged) half fresh — so the
    // prefetch itself carries no tagged content that an invalidation could make
    // stale. Serving it from the prerender even when a tag it shares was
    // invalidated is the stale-while-revalidate contract, and the real
    // navigation below still gates on the tag and wakes the Lambda, which
    // rewrites the entry. Gating prefetches on tags instead strands every route
    // under an invalidated tag on the Lambda, starving the client's segment
    // cache and blocking the very navigation the prefetch existed to make
    // instant.

    // A segment prefetch (Next's client segment cache) asks for one prerendered
    // segment by path. It is static build output, held in the entry's
    // segmentData — never composed and never resumed. Only the segment can
    // answer it: an unknown path falls open rather than serving the whole-page
    // shell, which is a different variant. This is the whole reason a PPR route
    // can be prefetched at the edge — the Lambda carries no prerender output to
    // serve it from, and would 404 the request.
    const segmentPath = request.headers.get("next-router-segment-prefetch");
    if (segmentPath !== null && value.kind === "APP_PAGE") {
      const response = reconstructSegment(value, segmentPath);
      if (!response) return null;
      response.headers.set("cache-control", `s-maxage=${window}`);
      return { kind: "complete", response, stale: false };
    }

    // A full-route prefetch (Next's router prefetch, distinct from the segment
    // cache above) wants only the static shell — never the per-visitor dynamic
    // resume. A PPR entry answers it from its prerendered shell, served cacheable
    // so the client's router cache holds it and the eventual navigation reveals
    // the shell instantly instead of blocking on a resumed render. Resuming here
    // would return a no-store body the client cannot cache, leaving the click
    // with no shell to reveal.
    // Only next-router-prefetch: 1 is a static prefetch. Next emits 2/3 for
    // runtime prefetches that intentionally perform a dynamic request, so those
    // must fall through to a real render rather than be handed the static shell.
    const prefetchMode = request.headers.get("next-router-prefetch");
    const isPrefetch = prefetchMode === "1";
    if (
      isPrefetch &&
      value.kind === "APP_PAGE" &&
      value.postponed !== undefined
    ) {
      const response = reconstruct(request, value);
      if (!response) return null;
      response.headers.set("cache-control", `s-maxage=${window}`);
      return { kind: "complete", response, stale: false };
    }

    // Runtime prefetch (2/3) intentionally requests a dynamic response; never
    // serve it from the cache.
    if (prefetchMode === "2" || prefetchMode === "3") {
      return null;
    }

    // Everything past here is a real request whose response *is* the tagged
    // content. An invalidated tag makes the entry stale, but — like a lapsed
    // revalidate window — it still serves stale-while-revalidate; the caller
    // wakes the Lambda in the background to rewrite it. Only an *untrusted* tag
    // snapshot declines to serve, because then staleness is unknown, and unknown
    // never serves.
    let tagStale = false;
    const tags = tagsOf(value, {});
    if (tags.length > 0) {
      const clock = deps.tagClock ?? createTagClock(cfg, deps);
      const verdict = await clock.expired(tags, entry.lastModified, now);
      if (verdict === "untrusted") return null; // R2 tier: unknown never serves.
      tagStale = verdict;
    }
    const isStale = stale || tagStale;

    // A stale entry still serves, but only inside its expiration window; past
    // that, even stale content is too old to serve and the request falls open.
    // Freshness never trips this — a fresh entry is always younger than its
    // expiration — so the gate only bites once the entry is already stale.
    if (
      isStale &&
      typeof target.expiration === "number" &&
      ageSeconds >= target.expiration
    ) {
      return null;
    }

    if (value.kind === "APP_PAGE" && value.postponed !== undefined) {
      const shell = reconstruct(request, value);
      return (
        shell && { kind: "ppr", shell, postponed: value.postponed, stale: isStale }
      );
    }

    const response = reconstruct(request, value);
    if (!response) return null;
    // A stale entry is served only until the background refresh rewrites it, so
    // it must not be memoized as fresh for the whole (possibly still large, when
    // only a tag invalidated it) remaining window — one second forces the next
    // request to re-read the by-then-refreshed entry.
    response.headers.set("cache-control", `s-maxage=${isStale ? 1 : window}`);
    return { kind: "complete", response, stale: isStale };
  } catch {
    return null;
  }
}

// isServable gates interception to the entry kinds it can rebuild a response
// from: an APP_PAGE, a PAGES html entry, or an APP_ROUTE body. FETCH entries and
// anything unrecognised forward to the Lambda.
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

// readFallbackShell is the one place a request is answered from a shell built
// for a different (param-agnostic) path, so it is also the one place to change
// if that turns out not to resume correctly for arbitrary params — an
// assumption still unproven against a real deploy. Only a postponed entry
// qualifies: a complete entry under the dynamic pattern would be another
// route's rendered page, not this one's.
async function readFallbackShell(
  cfg: InterceptionConfig,
  deps: InterceptDeps,
  target: InterceptTarget,
): Promise<CacheEntryFile | null> {
  if (!target.fallbackPath || target.fallbackPath === target.routePath) {
    return null;
  }
  const entry = await readEntry(cfg, deps, target.fallbackPath);
  return entry?.value?.postponed === undefined ? null : entry;
}

// readEntry fetches the entry object from the cache store, fronted by a
// per-isolate memo. A hot entry is served from memory; a stale one is served
// immediately and refreshed from R2 behind the request (or, without a waitUntil,
// re-read synchronously on the request that finds it stale).
async function readEntry(
  cfg: InterceptionConfig,
  deps: InterceptDeps,
  routePath: string,
): Promise<CacheEntryFile | null> {
  const key = `${cfg.prefix}/cache/${cacheKey(routePath)}.cache.json`;
  const now = (deps.now ?? Date.now)();
  const memo = entryMap(deps.store);

  const hit = memo.get(key);
  if (hit) {
    if (now - hit.at < entryMemoTtlMs) return hit.entry;
    if (deps.waitUntil) {
      refreshEntry(deps, key);
      return hit.entry;
    }
    // No background scheduler: re-read the store now, on this request. A hit
    // refreshes the memo; a miss evicts the stale entry and falls open to the
    // Lambda, so a re-read gap is never papered over with stale bytes.
    const fresh = await fetchEntry(deps.store, key);
    if (fresh) memoSet(memo, key, { at: now, entry: fresh });
    else memo.delete(key);
    return fresh ?? null;
  }

  const entry = await fetchEntry(deps.store, key);
  if (entry) memoSet(memo, key, { at: now, entry });
  return entry;
}

function entryMap(
  store: ObjectStoreReader,
): Map<string, { at: number; entry: CacheEntryFile }> {
  let map = entryMemo.get(store);
  if (!map) entryMemo.set(store, (map = new Map()));
  return map;
}

// Insertion-order LRU: re-inserting moves a key to the newest slot, so the
// oldest is always at the front to evict once the bound is exceeded.
function memoSet(
  map: Map<string, { at: number; entry: CacheEntryFile }>,
  key: string,
  value: { at: number; entry: CacheEntryFile },
): void {
  map.delete(key);
  map.set(key, value);
  if (map.size > entryMemoMax) {
    const oldest = map.keys().next().value;
    if (oldest !== undefined) map.delete(oldest);
  }
}

// refreshEntry re-reads one entry from R2 in the background, deduped per store so
// a burst of stale reads schedules a single refresh. A refresh that now misses
// evicts the memo, so the next request falls open to the Lambda.
function refreshEntry(deps: InterceptDeps, key: string): void {
  let pending = entryRefreshing.get(deps.store);
  if (!pending) entryRefreshing.set(deps.store, (pending = new Set()));
  if (pending.has(key)) return;
  pending.add(key);

  // The refresh must not touch the store on this tick — a macrotask boundary
  // (rather than a microtask one) is what actually keeps the R2 read off the
  // request that noticed the staleness, since the request's own promise chain
  // finishes draining microtasks before this ever runs.
  const run = new Promise<void>((resolve) => setTimeout(resolve, 0))
    .then(async () => {
      const entry = await fetchEntry(deps.store, key);
      const map = entryMap(deps.store);
      if (entry) memoSet(map, key, { at: (deps.now ?? Date.now)(), entry });
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

// headersFrom rebuilds a Headers from a stored variant map, dropping the internal
// tag header (the only header a client must never see). Every other header is
// replayed verbatim, so whatever Next stamped on the variant at build — including
// headers this worker has never heard of — reaches the client unchanged.
function headersFrom(map: Record<string, any> | undefined): Headers {
  const headers = new Headers();
  for (const [name, v] of Object.entries(map ?? {})) {
    if (name.toLowerCase() === "x-next-cache-tags") continue;
    headers.set(name, String(v));
  }
  return headers;
}

// reconstruct rebuilds the HTTP response Next would have served for this entry,
// negotiating RSC vs html and replaying that variant's stored headers verbatim:
// the html variant from value.headers, the RSC variant from value.rscHeaders,
// each captured at build from the prerender's own initialHeaders. Freshness is
// the caller's to declare: a complete entry gets its remaining revalidate
// window, a PPR shell gets no shared cache at all. The dispatch layer stamps the
// x-ocel-cache tier (PRERENDER) once it decides how the entry is served. An
// entry predating per-variant capture falls back to the negotiated content-type
// so it still serves. Returns null on an incomplete entry.
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
    // PAGES.
    headers = headersFrom(value.headers);
    body = value.html ?? "";
    if (!headers.has("content-type")) {
      headers.set("content-type", "text/html; charset=utf-8");
    }
  }

  return new Response(body, { status, headers });
}

// reconstructSegment answers a segment prefetch from the entry's stored
// segmentData, replaying the entry's segmentHeaders verbatim — the headers the
// client gates PPR support on, above all x-nextjs-postponed: 2. An entry with no
// segmentHeaders predates per-variant capture: rather than serve a segment
// missing that marker (which the client silently reads as "not PPR"), fall open
// so the next build or revalidation reseeds it. Returns null when the entry holds
// no segment under that path, leaving the caller to fall open.
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
