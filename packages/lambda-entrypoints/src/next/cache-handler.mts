import {
  awsCacheStore,
  type CacheEntryFile,
  type CacheStore,
  type TagRecord,
} from "./cache-store.mjs";
import {
  areTagsExpired,
  cacheKey,
  deserialize as deserializeBytes,
  tagsOf,
} from "@ocel/next-cache";
import { background } from "../shared/background.mjs";
import { recordAndPublish } from "./tag-clock.mjs";

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

// deserialize rebuilds the value Next expects from the stored JSON. The shared
// codec restores the binary payloads as Uint8Array; Next wrote and expects Node
// Buffers, so the bytes are re-wrapped (a Buffer view, no copy) to hand back
// exactly what it stored.
function deserialize(value: Record<string, any>): Record<string, any> {
  const out = deserializeBytes(value);
  if (out.body instanceof Uint8Array) out.body = toBuffer(out.body);
  if (out.rscData instanceof Uint8Array) out.rscData = toBuffer(out.rscData);
  if (out.segmentData instanceof Map) {
    for (const [path, bytes] of out.segmentData) {
      out.segmentData.set(path, toBuffer(bytes as Uint8Array));
    }
  }
  return out;
}

function toBuffer(bytes: Uint8Array): Buffer {
  return Buffer.from(bytes.buffer, bytes.byteOffset, bytes.byteLength);
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

  // The entry now lands in the store the edge reads, which is a cross-internet
  // PUT the response must not be held open for, so the write is deferred onto
  // the invocation. Nothing reads an entry back within the request that wrote
  // it; a write that never lands costs the cache entry and nothing else, and the
  // next request simply renders again.
  //
  // The value is serialized here, on the request path, because `data` carries a
  // live RenderResult that does not outlive the request that produced it.
  async set(key: string, data: any, ctx: any): Promise<void> {
    if (!data) return;
    try {
      const store = this.store;
      const value = serialize(data);
      if (data.kind === "FETCH") value.tags = ctx?.tags ?? [];
      const entry = { lastModified: Date.now(), value };
      const stored = cacheKey(key, ctx?.fetchCache ? "FETCH" : data.kind);
      background(() => store.writeEntry(stored, entry));
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

    // The shared clock is what publishes the edge's replica, and on an app with
    // no `use cache` anywhere nothing else ever reaches it. Deferred onto the
    // invocation so the request raising the invalidation does not pay for the
    // publish, and unable to throw, because Next hands this method through raw.
    background(() => recordAndPublish(list, record));
  }

  // No per-request memo is held, so there is nothing to reset.
  resetRequestCache(): void {}
}
