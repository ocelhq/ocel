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
  refreshHeader,
  tagsOf,
  variantHeadersFile,
} from "@framework/next-cache";
import { background } from "../shared/background.mjs";
import { noteRevalidation } from "./revalidation-signal.mjs";
import { recordTags, tagsExpireEntry } from "./tag-clock.mjs";

function unchunk(html: any): string {
  if (typeof html === "string") return html;
  if (Buffer.isBuffer(html)) return html.toString("utf8");
  if (typeof html?.toUnchunkedString === "function") {
    return html.toUnchunkedString();
  }
  return String(html ?? "");
}

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

function cacheControlOf(ctx: any): CacheEntryFile["cacheControl"] | undefined {
  const revalidate = ctx?.cacheControl?.revalidate;
  const expire = ctx?.cacheControl?.expire;
  if (typeof revalidate !== "number" && revalidate !== false) return undefined;
  return { revalidate, ...(typeof expire === "number" && { expire }) };
}

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

const RSC_REQUEST = Symbol.for("ocel.rsc-request");

function negotiateVariant(
  value: Record<string, any>,
  isRscRequest: boolean,
): Record<string, any> {
  if (!isRscRequest || value.kind !== "APP_PAGE") return value;
  if (value.rscHeaders) return { ...value, headers: value.rscHeaders };
  const { "content-type": _dropped, ...headers } = value.headers ?? {};
  return { ...value, headers };
}

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

export default class OcelCacheHandler {
  static store: CacheStore | undefined;

  static variantHeaders: Record<string, Record<string, unknown>> | undefined;

  private readonly requestHeaders: Record<string | symbol, any>;

  constructor(ctx?: { _requestHeaders?: Record<string, any> }) {
    this.requestHeaders = ctx?._requestHeaders ?? {};
  }

  private get isRscRequest(): boolean {
    return this.requestHeaders[RSC_REQUEST] === true || this.requestHeaders.rsc === "1";
  }

  private get refreshing(): number | undefined {
    const held = Number(this.requestHeaders[refreshHeader]);
    return Number.isFinite(held) ? held : undefined;
  }

  private get store(): CacheStore {
    return (OcelCacheHandler.store ??= awsCacheStore());
  }

  private get variantHeaders(): Record<string, Record<string, unknown>> {
    return (OcelCacheHandler.variantHeaders ??= loadVariantHeaders());
  }

  async get(key: string, ctx: any): Promise<CacheEntryFile | null> {
    try {
      const entry =
        ctx?.kind === "FETCH"
          ? await this.store.readFetch(key)
          : await this.store.readEntry(cacheKey(key));
      if (!entry) return null;
      if (ctx?.kind !== "FETCH" && entry.lastModified <= (this.refreshing ?? -Infinity)) {
        return null;
      }

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

  async set(key: string, data: any, ctx: any): Promise<void> {
    if (!data) return;
    try {
      const store = this.store;
      const value = serialize(data);
      if (data.kind === "FETCH") value.tags = ctx?.tags ?? [];
      if (data.kind === "APP_PAGE") {
        Object.assign(value, this.variantHeaders[cacheKey(key)]);
      }
      const isFetch = ctx?.fetchCache || data.kind === "FETCH";
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
    }
  }

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

    noteRevalidation();
    recordTags(list, record);
    await this.store.writeTags(list, record);
  }

  resetRequestCache(): void {}
}
