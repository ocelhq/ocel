// Cache interception: the worker reads the authoritative ISR cache directly from
// S3 (with DynamoDB tag parity) via SigV4-signed requests, so a cache hit skips
// the Lambda origin entirely. It mirrors OcelCacheHandler.get() using the shared
// @ocel/next-cache primitives, so the edge and the Lambda can never disagree
// about whether an entry is still servable.
//
// Interception is strictly additive: every miss, expiry, incomplete entry, or
// error returns null so the caller falls open to the existing Lambda path. A bug
// here can only ever cost the interception shortcut, never correctness.
import { AwsClient } from "aws4fetch";
import {
  areTagsExpired,
  cacheKey,
  deserialize,
  tagsOf,
  type CacheEntryFile,
  type TagRecord,
} from "@ocel/next-cache";

// The signed-request bindings the worker deploy injects. All-or-nothing: absent
// any one of them, interception is disabled and the worker forwards as before.
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
}

export interface InterceptDeps {
  // A SigV4-signing fetch (aws4fetch's AwsClient.fetch in production, a fake in
  // tests). Signs S3 GETs and DynamoDB BatchGetItem POSTs.
  signedFetch: typeof fetch;
  // Injected so freshness never depends on wall-clock time. Milliseconds.
  now?: () => number;
}

// A static entry (revalidate false/undefined) has no time-based expiry, only
// tag-based; it is memoized for a year, matching Next's own fully-static TTL.
const STATIC_WINDOW = 31536000;

// Tag reads sit on the request path, so the retry budget is deliberately small,
// mirroring the Lambda store: 50/100/200ms, then give up (fail open).
const batchGetMaxAttempts = 4;
const batchGetBackoffMs = 50;
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

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

// intercept attempts to serve a prerender target directly from S3+DynamoDB. It
// returns a reconstructed Response on a clean, fresh, unexpired hit, or null to
// fail open to the Lambda origin. It never throws.
export async function intercept(
  request: Request,
  target: InterceptTarget,
  cfg: InterceptionConfig,
  deps: InterceptDeps,
): Promise<Response | null> {
  try {
    const now = (deps.now ?? Date.now)();

    const entry = await readEntry(cfg, deps, target.routePath);
    if (!entry) return null;

    const value = entry.value;
    if (!isCompleteServable(value)) return null;

    const tags = tagsOf(value, {});
    if (tags.length > 0) {
      const records = await readTags(cfg, deps, tags);
      if (!records) return null; // DynamoDB read failed or was incomplete.
      if (areTagsExpired(tags, records, entry.lastModified, now)) return null;
    }

    if (typeof target.revalidate === "number") {
      const ageSeconds = (now - entry.lastModified) / 1000;
      if (ageSeconds >= target.revalidate) return null;
    }

    const revalidateWindow =
      typeof target.revalidate === "number" ? target.revalidate : STATIC_WINDOW;
    return reconstruct(request, value, revalidateWindow);
  } catch {
    return null;
  }
}

// isCompleteServable gates interception to the entry kinds it can serve in full:
// an APP_PAGE without a postponed (PPR) shell, a PAGES html entry, or an
// APP_ROUTE body. FETCH and partially-postponed entries forward to the Lambda.
function isCompleteServable(value: Record<string, any>): boolean {
  switch (value?.kind) {
    case "APP_PAGE":
      return value.postponed === undefined;
    case "PAGES":
    case "APP_ROUTE":
      return true;
    default:
      return false;
  }
}

// readEntry does the signed S3 GET of the entry object, returning null on a miss
// or any non-200 (fail open).
async function readEntry(
  cfg: InterceptionConfig,
  deps: InterceptDeps,
  routePath: string,
): Promise<CacheEntryFile | null> {
  const key = `${cfg.prefix}/cache/${cacheKey(routePath, undefined)}.cache.json`;
  const res = await deps.signedFetch(s3Url(cfg, key));
  if (!res.ok) return null;
  const entry = (await res.json()) as CacheEntryFile;
  if (!entry || typeof entry.lastModified !== "number" || !entry.value) {
    return null;
  }
  return entry;
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
// header, and cache-control is set to the full revalidate window so serveCached
// memoizes the hit for exactly that long. Returns null on an incomplete entry.
function reconstruct(
  request: Request,
  value: Record<string, any>,
  revalidateWindow: number,
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

  headers.set("cache-control", `s-maxage=${revalidateWindow}`);
  return new Response(body, { status, headers });
}

function s3Url(cfg: InterceptionConfig, key: string): string {
  const path = key.split("/").map(encodeURIComponent).join("/");
  return `https://${cfg.bucket}.s3.${cfg.region}.amazonaws.com/${path}`;
}

function ddbUrl(cfg: InterceptionConfig): string {
  return `https://dynamodb.${cfg.region}.amazonaws.com/`;
}
