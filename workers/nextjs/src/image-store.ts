// The durable tier under the colo cache for optimized images: one object per
// cache key, at images/<slug>/<cacheKey> and deliberately outside any buildId
// prefix, because the whole point of a content-hashed key is that an entry
// outlives the build that first produced it. No environment component either —
// preview and production already hold separate buckets.
//
// Writes go through the bound cache store rather than the signed S3 call the
// design of record specified; the PR 6 section of that document carries the
// amendment and the reason it could not be built as written.

import {
  imageStorable,
  refreshOnce,
  servedFromStore,
  NEXT_CACHE_STATUS,
  type CacheDeps,
} from "./cache";

// One stored object as the binding hands it back, and the write as it takes it.
// The Cloudflare R2 binding satisfies both directly, so nothing here names an
// edge.
export interface StoredImage {
  body: ReadableStream | null;
  customMetadata?: Record<string, string>;
}

export interface ImagePutOptions {
  httpMetadata?: { contentType?: string; cacheControl?: string };
  customMetadata?: Record<string, string>;
}

export interface ImageStore {
  get(key: string): Promise<StoredImage | null>;
  put(key: string, value: ArrayBuffer, options?: ImagePutOptions): Promise<unknown>;
}

// The version of the entry format, kept for the reason the colo tier keeps one:
// an entry this worker cannot fully trust is re-optimized, never reinterpreted.
// It is read before anything else is parsed, so a format change here is a miss
// rather than a misread, and needs no key change and no flush.
const ENTRY_VERSION = "ocel-image-version";
const ENTRY_FORMAT = "1";
// The served response's headers, as the single source of truth. R2's own
// httpMetadata models only the handful of fields S3 named — it has nowhere to
// keep Vary, the CSP, or Next's freshness header — so it is written beside this
// for anything reading the bucket directly, and never read back.
const ENTRY_HEADERS = "ocel-image-headers";

// The headers an entry may carry, governing the write and the read alike so the
// two cannot drift. An allowlist rather than a deny-list because a stored entry
// is not this worker's own word: the object key is a hash over inputs anyone can
// compute, and the R2 token behind the bucket is bucket-scoped rather than
// prefix-scoped and reaches every app's Lambda, so an entry planted under
// another tenant's images/ prefix is a reachable shape. Reconstructing from a
// fixed list is what keeps a planted `content-type: text/html` from being served
// as a document from this app's own origin on an unauthenticated path.
//
// Everything not named here is either about a body this tier decoded on the way
// in (content-encoding, content-length), or is decided per request by the tier
// that answers (the two cache statuses, and the colo tier's own bookkeeping).
const STORED_HEADERS = [
  "content-type",
  "cache-control",
  "etag",
  "content-disposition",
  "content-security-policy",
  "vary",
  NEXT_CACHE_STATUS,
];

// R2 caps an object's total custom metadata in the low kibibytes. Three of the
// stored headers are unbounded from here — the CSP is the app's config verbatim,
// the content-disposition filename derives from a `url` parameter admitted up to
// 3072 characters, and a passthrough etag is whatever a third-party server sent
// — so the budget is measured rather than assumed. Over it, R2 rejects the put,
// and a rejected put is indistinguishable from an outage: the tier disables
// itself for every image in the app, each one paying a full optimization
// forever, with nothing said.
const METADATA_BUDGET = 2048;

export function imageObjectKey(slug: string, digest: string): string {
  return `images/${slug}/${digest}`;
}

// A hit as the response the optimizer would have given, stamped PRERENDER — the
// status the colo tier above already promotes into a served status, and the one
// the epic reserves for the durable store. A miss, an unreadable entry and an
// unreachable store are the same answer: null, and the optimizer runs.
export async function readImage(
  store: ImageStore,
  key: string,
): Promise<Response | null> {
  let object: StoredImage | null;
  try {
    object = await store.get(key);
  } catch {
    return null;
  }
  if (!object) return null;

  const headers = storedHeaders(object.customMetadata);
  if (!headers || !object.body) {
    object.body?.cancel().catch(() => {});
    return null;
  }
  return servedFromStore(new Response(object.body, { headers }), false);
}

function storedHeaders(
  metadata: Record<string, string> | undefined,
): Headers | undefined {
  if (metadata?.[ENTRY_VERSION] !== ENTRY_FORMAT) return undefined;
  let stored: unknown;
  try {
    stored = JSON.parse(metadata[ENTRY_HEADERS] ?? "");
  } catch {
    return undefined;
  }
  if (typeof stored !== "object" || stored === null || Array.isArray(stored)) {
    return undefined;
  }

  const headers = new Headers();
  for (const name of STORED_HEADERS) {
    const value = (stored as Record<string, unknown>)[name];
    if (typeof value === "string") headers.set(name, value);
  }
  // Unconditional, so it is a property of this tier and not of the blob: an
  // entry that arrived without it is exactly the entry it protects against.
  headers.set("x-content-type-options", "nosniff");
  return headers;
}

function storedMetadata(response: Response): Record<string, string> {
  const headers: Record<string, string> = {};
  for (const name of STORED_HEADERS) {
    const value = response.headers.get(name);
    if (value !== null) headers[name] = value;
  }
  return {
    [ENTRY_VERSION]: ENTRY_FORMAT,
    [ENTRY_HEADERS]: JSON.stringify(headers),
  };
}

function metadataSize(metadata: Record<string, string>): number {
  const encoder = new TextEncoder();
  let total = 0;
  for (const [name, value] of Object.entries(metadata)) {
    total += encoder.encode(name).length + encoder.encode(value).length;
  }
  return total;
}

async function writeImage(
  store: ImageStore,
  key: string,
  response: Response,
): Promise<void> {
  const customMetadata = storedMetadata(response);
  const size = metadataSize(customMetadata);
  if (size > METADATA_BUDGET) {
    response.body?.cancel().catch(() => {});
    console.warn(
      `ocel: not storing the optimized image ${key} — its ${size} bytes of metadata exceed the ${METADATA_BUDGET}-byte budget`,
    );
    return;
  }

  await store.put(key, await response.arrayBuffer(), {
    httpMetadata: {
      contentType: response.headers.get("content-type") ?? undefined,
      cacheControl: response.headers.get("cache-control") ?? undefined,
    },
    customMetadata,
  });
}

// The optimizer with the write behind it, and nothing read. Everything both
// origins do after the optimizer runs lives here so the two cannot disagree
// about what is worth storing.
function optimizeAndStore(
  store: ImageStore,
  cache: CacheDeps,
  key: string,
  origin: () => Promise<Response>,
): () => Promise<Response> {
  return async () => {
    const optimized = await origin();
    // Gated on what the colo tier stores by, plus the status it refuses: a 502
    // from a substrate with no optimizer must not become the answer this tier
    // gives for a day. Fire and forget — a write that fails costs a future
    // cache miss and nothing else, and never the response in hand.
    if (optimized.status === 200 && imageStorable(optimized)) {
      // Cloned before serveCachedImage's own policy.forServe consumes the body.
      const copy = optimized.clone();
      // Dedup on the object key, which the colo tier's own dedup never uses, so
      // a burst of concurrent misses on one image issues a single put.
      refreshOnce(cache, key, () =>
        writeImage(store, key, copy).catch((error: unknown) => {
          console.warn(`ocel: could not store the optimized image ${key}`, error);
        }),
      );
    }
    return optimized;
  };
}

// The tier expressed as a wrapper around the optimizer rather than as a branch
// inside the colo cache. A hit answers in the optimizer's place carrying
// PRERENDER, which the colo tier already reads as a served status and already
// stores on the way past — so the read order becomes colo -> R2 -> optimizer,
// and a colo miss writes colo and this tier both, with neither tier knowing the
// other exists.
//
// The mirrored colo entry is stamped with the time of this serve, not of the
// write below it. An image entry is never aged out — the tier below holds the
// same bytes under the same key, so falling through to it would trade a served
// image for a blocking optimization — which leaves the stale refresh as the only
// path back to the optimizer, and it must not come through here. See
// durableImageRefresh.
export function durableImageOrigin(
  store: ImageStore,
  cache: CacheDeps,
  key: string,
  origin: () => Promise<Response>,
): () => Promise<Response> {
  const write = optimizeAndStore(store, cache, key, origin);
  return async () => (await readImage(store, key)) ?? write();
}

// The same tier for the colo cache's background refresh: the read is skipped, so
// the optimizer runs and both tiers are rewritten from what it produced. Reading
// here would answer the refresh with the bytes that made the entry stale in the
// first place, and since an image entry never expires, no later request could
// reach the optimizer either.
//
// Only a remote source needs it. A local image's key commits to a build-time
// content hash of the file itself, so re-optimizing could only ever reproduce
// the bytes already stored — a wasted optimization per key per window. A remote
// image's key commits to a normalized url, which stays put while the bytes
// behind it do not, so those have to be re-derived.
export const durableImageRefresh = optimizeAndStore;
