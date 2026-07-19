// Cache interception: the worker reads the authoritative ISR cache itself, so a
// cache hit skips the Lambda origin entirely. It mirrors OcelCacheHandler.get()
// using the shared @ocel/next-cache primitives, so the edge and the Lambda can
// never disagree about whether an entry is still servable.
//
// There are two read paths, chosen by whether the deploy bound a cache store:
//
//   • Bound. Entries come from the binding and tag state from the tag-clock
//     replica sitting beside them, so an interception calls exactly one service
//     and never an AWS API.
//   • Unbound. The original SigV4-signed S3 GET plus DynamoDB BatchGetItem, kept
//     verbatim so a substrate that never adopted a store keeps intercepting
//     rather than silently losing the shortcut.
//
// The second is a rollback path, not a fallback: a failed read on the bound path
// falls open to the origin like every other failure here, and never reaches for
// AWS behind the store's back.
//
// Interception is strictly additive: every miss, expiry, incomplete entry, or
// error returns null so the caller falls open to the existing Lambda path. A bug
// here can only ever cost the interception shortcut, never correctness.
import { AwsClient } from "aws4fetch";
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

// The signed-request bindings the worker deploy injects. All-or-nothing: absent
// any one of them, interception is disabled and the worker forwards as before.
// They are injected even on a substrate with a cache store, which is why the
// binding rather than their absence is what selects the read path.
export interface InterceptionConfig {
  accessKeyId: string;
  secretKey: string;
  region: string;
  bucket: string;
  table: string;
  prefix: string;
  tagNamespace: string;
}

// The prerender facts interception needs: the concrete pathname keying the S3
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
  // A SigV4-signing fetch (aws4fetch's AwsClient.fetch in production, a fake in
  // tests). Signs S3 GETs and DynamoDB BatchGetItem POSTs.
  signedFetch: typeof fetch;
  // The bound cache store. Present is what moves entries and tag state off AWS;
  // absent leaves the signed path below in charge.
  store?: ObjectStoreReader;
  // The PoP cache fronting the snapshot read. Absent, or inert as it is on
  // *.workers.dev, costs one store read per interception and nothing else.
  snapshotCache?: SnapshotCache;
  // Injected so freshness never depends on wall-clock time. Milliseconds.
  now?: () => number;
}

// A static entry (revalidate false/undefined) has no time-based expiry, only
// tag-based; it is memoized for a year, matching Next's own fully-static TTL.
const STATIC_WINDOW = 31536000;

// Stamped on every response reconstructed from the S3/DynamoDB read so an
// interception serve is distinguishable from a Lambda-origin one. Orthogonal to
// x-ocel-cache (the colo cache status): its absence means the fill came from the
// Lambda.
const ISR_STATUS = "x-ocel-isr";

// Tag reads sit on the request path, so the retry budget is deliberately small,
// mirroring the Lambda store: 50/100/200ms, then give up (fail open).
const batchGetMaxAttempts = 4;
const batchGetBackoffMs = 50;
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

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

// readInterceptionConfig reads the worker's edge bindings, returning null unless
// every one is present so interception stays all-or-nothing.
export function readInterceptionConfig(
  env: Record<string, string | undefined>,
): InterceptionConfig | null {
  const accessKeyId = env.OCEL_EDGE_ACCESS_KEY_ID;
  const secretKey = env.OCEL_EDGE_SECRET_KEY;
  const region = env.OCEL_AWS_REGION;
  const bucket = env.OCEL_ISR_BUCKET;
  const table = env.OCEL_STATE_TABLE;
  const prefix = env.OCEL_ISR_PREFIX;
  const tagNamespace = env.OCEL_ISR_TAG_NAMESPACE;
  if (
    !accessKeyId ||
    !secretKey ||
    !region ||
    !bucket ||
    !table ||
    !prefix ||
    !tagNamespace
  ) {
    return null;
  }
  return { accessKeyId, secretKey, region, bucket, table, prefix, tagNamespace };
}

// signerFor builds the production signed-fetch from an interception config.
export function signerFor(cfg: InterceptionConfig): typeof fetch {
  const aws = new AwsClient({
    accessKeyId: cfg.accessKeyId,
    secretAccessKey: cfg.secretKey,
    region: cfg.region,
  });
  return ((input: RequestInfo | URL, init?: RequestInit) =>
    aws.fetch(input as string, init)) as typeof fetch;
}

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

    const tags = tagsOf(value, {});
    if (tags.length > 0) {
      const records = deps.store
        ? await snapshotRecords(cfg, deps, deps.store, now)
        : await readTags(cfg, deps, tags);
      // The replica could not be trusted, or the DynamoDB read failed or came
      // back partial. Either way tag state is unknown, and unknown never serves.
      if (!records) return null;
      if (areTagsExpired(tags, records, entry.lastModified, now)) return null;
    }

    const ageSeconds = (now - entry.lastModified) / 1000;
    const revalidate =
      typeof target.revalidate === "number" ? target.revalidate : undefined;
    const stale = revalidate !== undefined && ageSeconds >= revalidate;

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
    const window =
      revalidate !== undefined
        ? Math.max(1, revalidate - Math.floor(ageSeconds))
        : STATIC_WINDOW;
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
// if that turns out not to resume correctly for arbitrary params — the
// assumption is unproven until it runs against a real deploy (bd ocelhq-jpx).
// Only a postponed entry qualifies: a complete entry under the dynamic pattern
// would be another route's rendered page, not this one's.
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

// readEntry fetches the entry object through whichever path is configured. The
// key layout is identical either way, so the two stores hold the same objects
// under the same names and a substrate can move between them without a backfill.
async function readEntry(
  cfg: InterceptionConfig,
  deps: InterceptDeps,
  routePath: string,
): Promise<CacheEntryFile | null> {
  const key = `${cfg.prefix}/cache/${cacheKey(routePath)}.cache.json`;
  const body = deps.store
    ? await storeText(deps.store, key)
    : await signedText(cfg, deps, key);
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

async function signedText(
  cfg: InterceptionConfig,
  deps: InterceptDeps,
  key: string,
): Promise<string | null> {
  const res = await deps.signedFetch(s3Url(cfg, key));
  return res.ok ? res.text() : null;
}

function parseJson<T>(body: string): T | null {
  try {
    return JSON.parse(body) as T;
  } catch {
    return null;
  }
}

// snapshotRecords is the bound path's answer to readTags: the tag clock as the
// last publisher left it, rather than a live read of the authoritative one.
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

// readTags mirrors the Lambda store's BatchGetItem read: 100 keys per call, with
// a bounded retry that drains UnprocessedKeys. Any error or an undrainable batch
// returns null so the caller fails open rather than serving on a partial read —
// a dropped tag record reads as "not invalidated", which would serve stale.
async function readTags(
  cfg: InterceptionConfig,
  deps: InterceptDeps,
  tags: string[],
): Promise<Map<string, TagRecord> | null> {
  const found = new Map<string, TagRecord>();
  for (let i = 0; i < tags.length; i += 100) {
    let keys = tags.slice(i, i + 100).map((tag) => ({
      pk: { S: cfg.tagNamespace + tag },
      sk: { S: "#META" },
    }));

    for (let attempt = 0; keys.length > 0; attempt++) {
      if (attempt === batchGetMaxAttempts) return null;
      if (attempt > 0) await sleep(batchGetBackoffMs << (attempt - 1));

      const res = await deps.signedFetch(ddbUrl(cfg), {
        method: "POST",
        headers: {
          "content-type": "application/x-amz-json-1.0",
          "x-amz-target": "DynamoDB_20120810.BatchGetItem",
        },
        body: JSON.stringify({ RequestItems: { [cfg.table]: { Keys: keys } } }),
      });
      if (!res.ok) return null;
      const json: any = await res.json();

      for (const item of json.Responses?.[cfg.table] ?? []) {
        const pk = item.pk?.S;
        if (!pk) continue;
        found.set(pk.slice(cfg.tagNamespace.length), {
          stale: item.stale?.N ? Number(item.stale.N) : undefined,
          expired: item.expired?.N ? Number(item.expired.N) : undefined,
        });
      }
      keys = json.UnprocessedKeys?.[cfg.table]?.Keys ?? [];
    }
  }
  return found;
}

// reconstruct rebuilds the HTTP response Next would have served for this entry,
// negotiating RSC vs html and deriving each variant's content-type the way Next
// does (an APP_PAGE stores html and RSC under one entry with the content-type
// stripped). The stored headers are carried through, minus the internal tag
// header. An x-ocel-isr: HIT marker is stamped so the serve is distinguishable
// from a Lambda-origin one. Freshness is the caller's to declare: a complete
// entry gets its remaining revalidate window, a PPR shell gets no shared cache
// at all. Returns null on an incomplete entry.
function reconstruct(
  request: Request,
  value: Record<string, any>,
): Response | null {
  const restored = deserialize(value);
  const headers = new Headers();
  for (const [name, v] of Object.entries(value.headers ?? {})) {
    if (name.toLowerCase() === "x-next-cache-tags") continue;
    headers.set(name, String(v));
  }
  const status = typeof value.status === "number" ? value.status : 200;

  let body: BodyInit;
  if (value.kind === "APP_ROUTE") {
    body = restored.body ?? new Uint8Array();
    // APP_ROUTE keeps its own content-type from the stored headers verbatim.
  } else if (value.kind === "APP_PAGE") {
    headers.set(
      "vary",
      "RSC, Next-Router-State-Tree, Next-Router-Prefetch, Next-Url",
    );
    if (request.headers.get("RSC") === "1") {
      if (!restored.rscData) return null; // Negotiated RSC but the entry has none.
      body = restored.rscData;
      headers.set("content-type", "text/x-component");
    } else {
      body = value.html ?? "";
      headers.set("content-type", "text/html; charset=utf-8");
    }
  } else {
    // PAGES.
    body = value.html ?? "";
    headers.set("content-type", "text/html; charset=utf-8");
  }

  headers.set(ISR_STATUS, "HIT");
  return new Response(body, { status, headers });
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

function s3Url(cfg: InterceptionConfig, key: string): string {
  return `https://${cfg.bucket}.s3.${cfg.region}.amazonaws.com/${encodeKeyPath(key)}`;
}

function ddbUrl(cfg: InterceptionConfig): string {
  return `https://dynamodb.${cfg.region}.amazonaws.com/`;
}
