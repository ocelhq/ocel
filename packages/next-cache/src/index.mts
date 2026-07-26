// Portable ISR cache primitives shared by the Lambda cache handler (which backs
// Next's server cache) and the Cloudflare worker (which reads the same
// authoritative cache directly at the edge, and writes tag records back into
// it). Everything here is runtime-neutral: no Node Buffer, no AWS SDK, no
// Workers globals beyond atob/btoa — so the key normalization, tag-expiry,
// payload-decoding, tag-write and snapshot-merge rules are single-sourced and
// the two sides can never disagree on them. The transport around these (the
// S3/DynamoDB calls) stays per-side, since one speaks the AWS SDK and the
// other signs raw HTTP.

export {
  isGuardRejection,
  tagRecordUpdate,
  tagSortKey,
  type TagAttribute,
  type TagRecordUpdate,
  type TagUpdateItem,
} from "./tag-index.mjs";
export {
  latest,
  mergeRecord,
  mergeSnapshot,
  publishTagSnapshot,
  type StoredTagSnapshot,
  type TagSnapshotStore,
} from "./tag-snapshot.mjs";
export type { EdgeCacheRpc, FetchCacheEntry } from "./edge-cache-rpc.mjs";

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

// The tag clock as the edge reads it: a replica of the authoritative DynamoDB
// clock, published per build by whichever Lambda last drained the index.
//
// `deployedAt` is the build's own deploy time and is set once, at genesis, then
// carried forward unchanged. It is what makes pruning provable: every entry in a
// build has lastModified >= deployedAt, so a record whose watermarks both sit at
// or before it can no longer expire anything. Zero means the snapshot was never
// anchored — created by a publisher rather than seeded by the deploy — and
// nothing may be pruned from it.
//
// The document carries no expiry. A replica is current until a publisher says
// otherwise, and a publisher republishes on every invalidation it observes — so
// an unchanged object means nothing has changed, not that nobody has looked
// lately. `generatedAt` records when it was last written, for operators reading
// the object; no reader branches on it.
export interface TagSnapshot {
  version: 1;
  deployedAt: number;
  generatedAt: number;
  records: Record<string, TagRecord>;
}

// Where the snapshot sits: beside the build's cache entries, under the same
// prefix the deploy scopes every other object to. Both readers call this, but
// the deploy that seeds the object is Go and spells the suffix out itself — so
// nothing about calling one function holds them together. What does is the
// checked-in edge contract fixture, whose suffix both languages assert their own
// spelling against.
export function tagSnapshotKey(prefix: string): string {
  return `${prefix}/tag-clock.json`;
}

// The partition-key prefix an app's tag records live under in the state table,
// derived from the same ISR prefix that scopes its objects so one identity
// governs both stores — which is also what lets the deploy grant DynamoDB with a
// single LeadingKeys wildcard.
//
// Held together with Go by the same fixture tagSnapshotKey is, and for a stronger
// reason: the Lambda tier is handed the finished namespace in its env and never
// derives it, so the deploy and this function are the only two spellings and
// nothing at runtime would notice them drifting.
export function tagNamespace(prefix: string): string {
  return `TAG#${prefix.replaceAll("/", "#")}#`;
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
// time. Route entries only: fetch entries are keyed by their own hash into a
// separate, AWS-private bucket, which no edge reader can reach — so their keying
// lives with the Lambda store rather than in this shared module.
export function cacheKey(key: string): string {
  return key === "/" || key === "" ? "index" : key.replace(/^\//, "");
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
