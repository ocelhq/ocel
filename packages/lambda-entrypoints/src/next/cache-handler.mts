import { readFileSync } from "node:fs";
import { join } from "node:path";

import {
  awsCacheStore,
  type CacheEntryFile,
  type CacheStore,
  type TagRecord,
} from "./cache-store.mjs";
import {
  cacheKey,
  deserialize as deserializeBytes,
  tagsOf,
  variantHeadersFile,
} from "@ocel/next-cache";
import { background } from "../shared/background.mjs";
import { recordTags, tagsExpireEntry } from "./tag-clock.mjs";

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

// cacheControlOf reads the window Next declared for this render, narrowed to the
// two fields the edge ages an entry by. An unrecognised shape records nothing,
// leaving the routing manifest's window to stand in as it did before.
function cacheControlOf(ctx: any): CacheEntryFile["cacheControl"] | undefined {
  const revalidate = ctx?.cacheControl?.revalidate;
  const expire = ctx?.cacheControl?.expire;
  if (typeof revalidate !== "number" && revalidate !== false) return undefined;
  return { revalidate, ...(typeof expire === "number" && { expire }) };
}

// A route with no projected headers is a route the build never prerendered, and
// an unreadable projection is a bundle that would have had nothing to reseed
// from anyway — neither is a reason to fail a write.
function loadVariantHeaders(): Record<string, Record<string, unknown>> {
  const root = process.env.LAMBDA_TASK_ROOT ?? process.cwd();
  try {
    const parsed = JSON.parse(readFileSync(join(root, variantHeadersFile), "utf8"));
    return isProjection(parsed) ? parsed : {};
  } catch {
    return {};
  }
}

function isProjection(
  parsed: unknown,
): parsed is Record<string, Record<string, unknown>> {
  return typeof parsed === "object" && parsed !== null && !Array.isArray(parsed);
}

// The membrane's mark, set on the request headers before Next runs. Registered
// so this bundle and the entrypoint's resolve to the same symbol.
const RSC_REQUEST = Symbol.for("ocel.rsc-request");

// negotiateVariant answers an RSC request with the RSC variant's own headers.
// Next replays the entry's `headers` verbatim onto the response and
// send-payload only fills in a content-type that is not already set, so an RSC
// request served from an APP_PAGE entry would go out labelled
// `text/html` — which the client router reads as "not flight data" and answers
// with a full document reload instead of the soft navigation. The prerender
// stores the RSC variant's headers alongside the html one; hand those back
// instead, exactly as the edge worker negotiates. An entry predating
// per-variant capture has no rscHeaders: drop the html content-type and let
// send-payload derive it from the flight payload.
function negotiateVariant(
  value: Record<string, any>,
  isRscRequest: boolean,
): Record<string, any> {
  if (!isRscRequest || value.kind !== "APP_PAGE") return value;
  if (value.rscHeaders) return { ...value, headers: value.rscHeaders };
  const { "content-type": _dropped, ...headers } = value.headers ?? {};
  return { ...value, headers };
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
// bucket (entries) and state table (the durable raise a revalidateTag makes), so
// ISR survives a cold sandbox and an invalidation reaches every instance rather
// than just the one that served the call. Nothing here reads the table back: the
// raise leaves through the stream's publisher and returns as this build's tag
// snapshot, which the shared tag clock is what reads.
export default class OcelCacheHandler {
  // Bound lazily so importing this module never reaches for AWS or its env, and
  // so tests can drive the cache semantics against a fake.
  static store: CacheStore | undefined;

  // Read from the bundle on first use, and overridable so a test can drive the
  // rewrite against a build's projection without one on disk.
  static variantHeaders: Record<string, Record<string, unknown>> | undefined;

  // Next constructs the handler once per request and hands it that request's
  // headers, which is the only way a cached entry can be served as the variant
  // the client actually asked for.
  private readonly requestHeaders: Record<string | symbol, any>;

  constructor(ctx?: { _requestHeaders?: Record<string, any> }) {
    this.requestHeaders = ctx?._requestHeaders ?? {};
  }

  // `rsc` is gone by the time a prerendered route reaches here — Next strips the
  // flight headers off this very object first — so the membrane's mark is the
  // signal that survives. The header is still honoured for every path Next
  // leaves intact.
  private get isRscRequest(): boolean {
    return this.requestHeaders[RSC_REQUEST] === true || this.requestHeaders.rsc === "1";
  }

  private get store(): CacheStore {
    return (OcelCacheHandler.store ??= awsCacheStore());
  }

  private get variantHeaders(): Record<string, Record<string, unknown>> {
    return (OcelCacheHandler.variantHeaders ??= loadVariantHeaders());
  }

  // Next does not wrap get() in a try/catch: a throw surfaces as a render error
  // rather than a miss. Every failure is therefore swallowed into null, which
  // degrades a cache outage into a fresh render instead of an outage.
  async get(key: string, ctx: any): Promise<CacheEntryFile | null> {
    try {
      // Next reports the fetch kind differently on each side — ctx.kind here,
      // ctx.fetchCache on set — so the two predicates differ deliberately.
      const entry =
        ctx?.kind === "FETCH"
          ? await this.store.readFetch(key)
          : await this.store.readEntry(cacheKey(key));
      if (!entry) return null;

      const tags = tagsOf(entry.value, ctx);
      if (tags.length > 0 && (await tagsExpireEntry(tags, entry.lastModified))) {
        return null;
      }
      const value = negotiateVariant(entry.value, this.isRscRequest);
      return { lastModified: entry.lastModified, value: deserialize(value) };
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
      // Reseeded from the build's projection, never from the entry being
      // replaced — see variantHeaderProjection in the adapter for why.
      if (data.kind === "APP_PAGE") {
        Object.assign(value, this.variantHeaders[cacheKey(key)]);
      }
      const isFetch = ctx?.fetchCache || data.kind === "FETCH";
      // The route's freshness window, recorded on the entry. For a path
      // generated on demand this is the only place it is ever known: the build
      // manifest names only the paths the build prerendered, so without it the
      // edge has nothing to age the entry by. A fetch entry carries its own
      // revalidate inside the value and is never given one.
      const cacheControl = !isFetch && cacheControlOf(ctx);
      const entry = {
        lastModified: Date.now(),
        value,
        ...(cacheControl && { cacheControl }),
      };
      background(() =>
        isFetch
          ? store.writeFetch(key, entry)
          : store.writeEntry(cacheKey(key), entry),
      );
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

    // The durable write above is the raise; every other instance hears about it
    // through the state table. This one shares its clock with the `use cache`
    // handler, which would otherwise not see the invalidation until its next
    // sync — an in-memory merge, so there is nothing here to defer or to catch.
    recordTags(list, record);
  }

  // No per-request memo is held, so there is nothing to reset.
  resetRequestCache(): void {}
}
