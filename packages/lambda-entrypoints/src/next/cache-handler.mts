import {
  awsCacheStore,
  type CacheEntryFile,
  type CacheStore,
  type TagRecord,
} from "./cache-store.mjs";

// The header Next stamps a route's cache tags onto. For page and route kinds the
// tags reach us only this way: unlike the FETCH kind, their `get` context
// carries no tags at all, so the entry itself is the only record of what it
// depends on.
const TAGS_HEADER = "x-next-cache-tags";

// areTagsExpired mirrors Next's own tagsManifest check: a tag expires an entry
// only when its expiry has already passed *and* it landed after the entry was
// written. An expiry still in the future leaves the entry usable until then.
function areTagsExpired(
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

// tagsOf reports what a cached entry depends on. FETCH entries are told their
// tags per request; everything else carries them in the response headers Next
// stored alongside the body.
function tagsOf(value: Record<string, any>, ctx: any): string[] {
  if (value?.kind === "FETCH") {
    return [...(ctx?.tags ?? []), ...(ctx?.softTags ?? []), ...(value.tags ?? [])];
  }
  const header = value?.headers?.[TAGS_HEADER];
  return typeof header === "string" && header.length > 0 ? header.split(",") : [];
}

// unchunk flattens whatever Next hands us as a body into something storable. On
// `set` an html body is a RenderResult; on the way back out of S3 it is already
// a plain string.
function unchunk(html: any): string {
  if (typeof html === "string") return html;
  if (Buffer.isBuffer(html)) return html.toString("utf8");
  if (typeof html?.toUnchunkedString === "function") {
    return html.toUnchunkedString();
  }
  return String(html ?? "");
}

// serialize converts a live cache value into the JSON-safe shape stored in S3,
// base64-ing the binary payloads so an entry stays one object.
function serialize(data: any): Record<string, any> {
  const value: Record<string, any> = {
    kind: data.kind,
    headers: data.headers,
    status: data.status,
  };
  switch (data.kind) {
    case "APP_ROUTE":
      value.body = Buffer.from(data.body ?? "").toString("base64");
      break;
    case "APP_PAGE":
      value.html = unchunk(data.html);
      if (data.rscData) value.rscData = Buffer.from(data.rscData).toString("base64");
      if (data.postponed !== undefined) value.postponed = data.postponed;
      if (data.segmentData) {
        const segments: Record<string, string> = {};
        for (const [path, buf] of data.segmentData) {
          segments[path] = Buffer.from(buf).toString("base64");
        }
        value.segmentData = segments;
      }
      break;
    case "PAGES":
      value.html = unchunk(data.html);
      value.pageData = data.pageData;
      break;
    case "FETCH":
      value.data = data.data;
      value.revalidate = data.revalidate;
      value.tags = data.tags;
      break;
    default:
      return { ...data };
  }
  return value;
}

// deserialize rebuilds the value Next expects from the stored JSON, restoring
// the binary payloads the entry base64'd on the way in.
function deserialize(value: Record<string, any>): Record<string, any> {
  const out: Record<string, any> = { ...value };
  if (value.kind === "APP_ROUTE" && typeof value.body === "string") {
    out.body = Buffer.from(value.body, "base64");
  }
  if (value.kind === "APP_PAGE") {
    out.rscData = value.rscData
      ? Buffer.from(value.rscData, "base64")
      : undefined;
    if (value.segmentData) {
      out.segmentData = new Map(
        Object.entries(value.segmentData as Record<string, string>).map(
          ([path, b64]) => [path, Buffer.from(b64, "base64")],
        ),
      );
    }
  }
  return out;
}

// cacheKey turns Next's key into the object name the adapter seeded at build
// time. Fetch entries share the namespace with routes, so they are kept under
// their own folder rather than risking a collision with a route of the same name.
function cacheKey(key: string, kind: string | undefined): string {
  const normalized = key === "/" || key === "" ? "index" : key.replace(/^\//, "");
  return kind === "FETCH" ? `__fetch__/${normalized}` : normalized;
}

// OcelCacheHandler backs Next's server cache with the account-global asset
// bucket (entries) and state table (tag invalidations), so ISR survives a cold
// sandbox and revalidateTag reaches every instance rather than just the one that
// served the call.
export default class OcelCacheHandler {
  // Bound lazily so importing this module never reaches for AWS or its env, and
  // so tests can drive the cache semantics against a fake.
  static store: CacheStore | undefined;

  private get store(): CacheStore {
    return (OcelCacheHandler.store ??= awsCacheStore());
  }

  // Next does not wrap get() in a try/catch: a throw surfaces as a render error
  // rather than a miss. Every failure is therefore swallowed into null, which
  // degrades a cache outage into a fresh render instead of an outage.
  async get(key: string, ctx: any): Promise<CacheEntryFile | null> {
    try {
      const entry = await this.store.readEntry(cacheKey(key, ctx?.kind));
      if (!entry) return null;

      const tags = tagsOf(entry.value, ctx);
      if (tags.length > 0) {
        const records = await this.store.readTags(tags);
        if (areTagsExpired(tags, records, entry.lastModified, Date.now())) {
          return null;
        }
      }
      return { lastModified: entry.lastModified, value: deserialize(entry.value) };
    } catch {
      return null;
    }
  }

  // set() runs after the response is already streaming, so a failure costs the
  // cache entry and nothing else — the next request simply renders again.
  async set(key: string, data: any, ctx: any): Promise<void> {
    if (!data) return;
    try {
      const value = serialize(data);
      if (data.kind === "FETCH") value.tags = ctx?.tags ?? [];
      await this.store.writeEntry(cacheKey(key, ctx?.fetchCache ? "FETCH" : data.kind), {
        lastModified: Date.now(),
        value,
      });
    } catch {
      // Swallowed deliberately: see above.
    }
  }

  // revalidateTag records the invalidation for every instance to observe, which
  // is the whole reason tags live in DynamoDB rather than in memory. It is O(#tags)
  // because Next never asks which paths carry a tag — entries check their own.
  async revalidateTag(
    tags: string | string[],
    durations?: { expire?: number },
  ): Promise<void> {
    const list = typeof tags === "string" ? [tags] : tags;
    if (list.length === 0) return;

    const now = Date.now();
    const record: TagRecord = durations
      ? {
          stale: now,
          ...(durations.expire !== undefined
            ? { expired: now + durations.expire * 1000 }
            : {}),
        }
      : { expired: now };

    await this.store.writeTags(list, record);
  }

  // No per-request memo is held, so there is nothing to reset.
  resetRequestCache(): void {}
}
