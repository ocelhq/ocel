// Cache interception: the worker reads the authoritative ISR cache itself, so a
// cache hit skips the Lambda origin entirely. It mirrors OcelCacheHandler.get()
// using the shared @ocel/next-cache primitives, so the edge and the Lambda can
// never disagree about whether an entry is still servable.
//
// Entries come from the bound R2 cache store, and tag state from the tag-clock
// snapshot sitting beside them, so an interception reads exactly one store and
// never an AWS API.
//
// Interception is strictly additive: every miss, expiry, incomplete entry, or
// error returns null so the caller falls open to the existing Lambda path. A bug
// here can only ever cost the interception shortcut, never correctness.
import {
  areTagsExpired,
  cacheKey,
  deserialize,
  tagSnapshotKey,
  tagsOf,
  type CacheEntryFile,
  type TagRecord,
  type TagSnapshot,
} from "@ocel/next-cache";

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
  | { kind: "complete"; response: Response }
  | { kind: "ppr"; shell: Response; postponed: string; stale: boolean };

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

export interface InterceptDeps {
  // The bound cache store entries and tag state are read from (the Cloudflare R2
  // binding in production, a fake in tests).
  store: ObjectStoreReader;
  // The PoP cache fronting the snapshot read. Absent, or inert as it is on
  // *.workers.dev, costs one store read per interception and nothing else.
  snapshotCache?: SnapshotCache;
  // Injected so freshness never depends on wall-clock time. Milliseconds.
  now?: () => number;
}

// A static entry (revalidate false/undefined) has no time-based expiry, only
// tag-based; it is memoized for a year, matching Next's own fully-static TTL.
const STATIC_WINDOW = 31536000;

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
      return { kind: "complete", response };
    }

    // A full-route prefetch (Next's router prefetch, distinct from the segment
    // cache above) wants only the static shell — never the per-visitor dynamic
    // resume. A PPR entry answers it from its prerendered shell, served cacheable
    // so the client's router cache holds it and the eventual navigation reveals
    // the shell instantly instead of blocking on a resumed render. Resuming here
    // would return a no-store body the client cannot cache, leaving the click
    // with no shell to reveal.
    const isPrefetch = request.headers.get("next-router-prefetch") !== null;
    if (
      isPrefetch &&
      value.kind === "APP_PAGE" &&
      value.postponed !== undefined
    ) {
      const response = reconstruct(request, value);
      if (!response) return null;
      response.headers.set("cache-control", `s-maxage=${window}`);
      return { kind: "complete", response };
    }

    // Everything past here is a real request whose response *is* the tagged
    // content, so an invalidated tag must send it to the Lambda to re-render.
    const tags = tagsOf(value, {});
    if (tags.length > 0) {
      const records = await snapshotRecords(cfg, deps, deps.store, now);
      // The snapshot could not be trusted, so tag state is unknown — and unknown
      // never serves.
      if (!records) return null;
      if (areTagsExpired(tags, records, entry.lastModified, now)) return null;
    }

    if (value.kind === "APP_PAGE" && value.postponed !== undefined) {
      // A stale PPR pair still serves — its dynamic half is rendered fresh
      // either way — until the expiration window closes on it entirely.
      if (
        typeof target.expiration === "number" &&
        ageSeconds >= target.expiration
      ) {
        return null;
      }
      const shell = reconstruct(request, value);
      return shell && { kind: "ppr", shell, postponed: value.postponed, stale };
    }

    if (stale) return null;

    const response = reconstruct(request, value);
    if (!response) return null;
    response.headers.set("cache-control", `s-maxage=${window}`);
    return { kind: "complete", response };
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

// readEntry fetches the entry object from the cache store.
async function readEntry(
  cfg: InterceptionConfig,
  deps: InterceptDeps,
  routePath: string,
): Promise<CacheEntryFile | null> {
  const key = `${cfg.prefix}/cache/${cacheKey(routePath)}.cache.json`;
  const body = await storeText(deps.store, key);
  if (body === null) return null;

  const entry = parseJson<CacheEntryFile>(body);
  if (!entry || typeof entry.lastModified !== "number" || !entry.value) {
    return null;
  }
  return entry;
}

async function storeText(
  store: ObjectStoreReader,
  key: string,
): Promise<string | null> {
  const object = await store.get(key);
  return object ? object.text() : null;
}

function parseJson<T>(body: string): T | null {
  try {
    return JSON.parse(body) as T;
  } catch {
    return null;
  }
}

// snapshotRecords resolves tag state from the tag clock as the last publisher
// left it, rather than a live read of the authoritative one.
async function snapshotRecords(
  cfg: InterceptionConfig,
  deps: InterceptDeps,
  store: ObjectStoreReader,
  now: number,
): Promise<Map<string, TagRecord> | null> {
  const snapshot = await readSnapshot(cfg, deps, store, now);
  return snapshot && new Map(Object.entries(snapshot.records));
}

// readSnapshot returns the build's tag-clock replica, or null whenever it cannot
// be trusted — missing, torn, stale, or written in a format this worker predates.
// Every one of those falls open to the origin, which wakes a Lambda, which
// republishes: the liveness loop repairs itself by being used.
async function readSnapshot(
  cfg: InterceptionConfig,
  deps: InterceptDeps,
  store: ObjectStoreReader,
  now: number,
): Promise<TagSnapshot | null> {
  const memoized = snapshotMemo.get(store);
  if (memoized && now - memoized.at < snapshotMemoMs) {
    return usableSnapshot(memoized.snapshot, now);
  }

  const key = tagSnapshotKey(cfg.prefix);
  const cacheRequest = new Request(snapshotCacheUrl(key));
  const cached = await matchSnapshot(deps.snapshotCache, cacheRequest);

  const body = cached ?? (await storeText(store, key));
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
  snapshotMemo.set(store, { at: now, snapshot });
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
