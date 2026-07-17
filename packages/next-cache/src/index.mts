// Portable ISR cache primitives shared by the Lambda cache handler (which backs
// Next's server cache) and the Cloudflare worker (which reads the same
// authoritative cache directly at the edge). Everything here is runtime-neutral:
// no Node Buffer, no AWS SDK, no Workers globals beyond atob/btoa — so the key
// normalization, tag-expiry, and payload-decoding rules are single-sourced and
// the two readers can never disagree on them. The transport around these (the
// S3/DynamoDB calls) stays per-reader, since one speaks the AWS SDK and the
// other signs raw HTTP.

// A cache entry exactly as it sits in S3: one object per route holding the html,
// the RSC payload and any PPR segments together, so a read is a single GET and a
// write is atomic. Binary bodies are base64 so the whole entry stays one JSON
// document.
export interface CacheEntryFile {
  lastModified: number;
  value: Record<string, any>;
}

// When a tag was last invalidated. Mirrors Next's own tagsManifest entries:
// `expired` marks the moment the tag's content stopped being usable, `stale`
// the moment it should be refreshed in the background.
export interface TagRecord {
  stale?: number;
  expired?: number;
}

// The header Next stamps a route's cache tags onto. For page and route kinds the
// tags reach a reader only this way — the entry itself is the only record of
// what it depends on.
const TAGS_HEADER = "x-next-cache-tags";

// base64ToBytes / bytesToBase64 are the runtime-neutral codec both readers use.
// atob/btoa exist in Node 20+ and the Workers runtime; they operate on binary
// strings (one char per byte), so the byte<->char loops are the bridge to and
// from a Uint8Array.
export function base64ToBytes(b64: string): Uint8Array {
  const binary = atob(b64);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
  return bytes;
}

export function bytesToBase64(bytes: Uint8Array): string {
  let binary = "";
  for (let i = 0; i < bytes.length; i++) binary += String.fromCharCode(bytes[i]!);
  return btoa(binary);
}

// cacheKey turns Next's key into the object name the adapter seeded at build
// time. Fetch entries share the namespace with routes, so they are kept under
// their own folder rather than risking a collision with a route of the same name.
export function cacheKey(key: string, kind: string | undefined): string {
  const normalized = key === "/" || key === "" ? "index" : key.replace(/^\//, "");
  return kind === "FETCH" ? `__fetch__/${normalized}` : normalized;
}

// tagsOf reports what a cached entry depends on. FETCH entries are told their
// tags per request; everything else carries them in the response headers Next
// stored alongside the body.
export function tagsOf(value: Record<string, any>, ctx: any): string[] {
  if (value?.kind === "FETCH") {
    return [...(ctx?.tags ?? []), ...(ctx?.softTags ?? []), ...(value.tags ?? [])];
  }
  const header = value?.headers?.[TAGS_HEADER];
  return typeof header === "string" && header.length > 0 ? header.split(",") : [];
}

// areTagsExpired mirrors Next's own tagsManifest check: a tag expires an entry
// only when its expiry has already passed *and* it landed after the entry was
// written. An expiry still in the future leaves the entry usable until then.
export function areTagsExpired(
  tags: string[],
  records: Map<string, TagRecord>,
  timestamp: number,
  now: number,
): boolean {
  for (const tag of tags) {
    const expiredAt = records.get(tag)?.expired;
    if (typeof expiredAt !== "number") continue;
    if (expiredAt <= now && expiredAt > timestamp) return true;
  }
  return false;
}

// deserialize rebuilds a cache value from its stored JSON, restoring the binary
// payloads the entry base64'd on the way in as Uint8Array. Callers that need
// Node Buffers (the Lambda handler, so Next sees exactly what it wrote) wrap the
// bytes themselves; the worker serves them straight into a Response.
export function deserialize(value: Record<string, any>): Record<string, any> {
  const out: Record<string, any> = { ...value };
  if (value.kind === "APP_ROUTE" && typeof value.body === "string") {
    out.body = base64ToBytes(value.body);
  }
  if (value.kind === "APP_PAGE") {
    out.rscData = value.rscData ? base64ToBytes(value.rscData) : undefined;
    if (value.segmentData) {
      out.segmentData = new Map(
        Object.entries(value.segmentData as Record<string, string>).map(
          ([path, b64]) => [path, base64ToBytes(b64)],
        ),
      );
    }
  }
  return out;
}
