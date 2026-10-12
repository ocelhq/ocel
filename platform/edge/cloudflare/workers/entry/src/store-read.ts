import {
  type CacheEntryFile,
  cacheKey,
  deserialize,
  entryObjectKey,
  tagsOf,
} from "@framework/next-cache";
import { storedCacheTags } from "@platform/edge-contract/cache-tags";
import { edgeCacheTagHeader } from "@platform/edge-contract/edge-cache";
import { POSTPONED_LENGTH_HEADER, PPR_SHELL_VARIANT } from "./ppr";
import type { TagTimes } from "./purge-times";

export const STORE_REVALIDATES_FOR_S = 31_536_000;

export const PURGE_SKEW_MS = 1_000;

export const ENTRY_SOURCE_HEADER = "x-ocel-entry-source";

export interface StoreSource {
  get(key: string): Promise<{ text(): Promise<string> } | null>;
}

export interface StoreWindow {
  revalidate?: number;
  expire?: number;
}

export interface StoreCall {
  isrPrefix: string;
  release: string;
  entryPath: string;
  fallbackPath?: string;
  window?: StoreWindow;
  variant: string;
  headers: Headers;
  sharedShell?: boolean;
  documentRequest?: boolean;
}

export interface StoreReadDeps {
  source: StoreSource;
  readTimes(tags: string[]): Promise<TagTimes>;
  now(): number;
}

export interface StoreAnswer {
  response: Response;
  stale: boolean;
  from: EntryFrom;
}

type Verdict = "fresh" | "stale" | "purged" | "expired";

function isServable(value: Record<string, any> | undefined): boolean {
  return value?.kind === "APP_PAGE" || value?.kind === "PAGES" || value?.kind === "APP_ROUTE";
}

function parseEntry(body: string): CacheEntryFile | null {
  try {
    const entry = JSON.parse(body) as CacheEntryFile;
    return typeof entry?.lastModified === "number" && isServable(entry.value) ? entry : null;
  } catch {
    return null;
  }
}

async function readEntry(deps: StoreReadDeps, call: StoreCall, path: string) {
  const key = entryObjectKey(call.isrPrefix, cacheKey(path));
  if (key === null) return null;
  const object = await deps.source.get(key);
  return object ? parseEntry(await object.text()) : null;
}

function segmentOf(headers: Headers): string | null {
  return headers.get("RSC") === null ? null : headers.get("next-router-segment-prefetch");
}

function hasSegment(value: Record<string, any>, segmentPath: string): boolean {
  return value.segmentHeaders !== undefined && value.segmentData?.[segmentPath] !== undefined;
}

function suitsRequest(entry: CacheEntryFile, call: StoreCall): boolean {
  const segment = segmentOf(call.headers);
  return segment === null || hasSegment(entry.value, segment);
}

export type EntryFrom = "own" | "fallback" | "lazy-fallback";

interface FoundEntry {
  entry: CacheEntryFile;
  from: EntryFrom;
}

function isLazyFallback(value: Record<string, any>, call: StoreCall): boolean {
  return (
    value.kind === "PAGES" && call.documentRequest === true && call.headers.get("RSC") === null
  );
}

async function findEntry(deps: StoreReadDeps, call: StoreCall): Promise<FoundEntry | null> {
  const concrete = await readEntry(deps, call, call.entryPath);
  const own = concrete && { entry: concrete, from: "own" as const };
  if (concrete && suitsRequest(concrete, call)) return own;
  if (call.fallbackPath === undefined || call.fallbackPath === call.entryPath) return own;
  const fallback = await readEntry(deps, call, call.fallbackPath);
  if (!fallback) return own;
  const shared = { entry: fallback, from: "fallback" as const };
  const segment = segmentOf(call.headers);
  if (segment !== null) return hasSegment(fallback.value, segment) ? shared : own;
  if (fallback.value.postponed !== undefined) return shared;
  return isLazyFallback(fallback.value, call) ? { ...shared, from: "lazy-fallback" } : own;
}

function judge(
  entry: CacheEntryFile,
  times: TagTimes,
  tags: string[],
  now: number,
  window: StoreWindow | undefined,
): Verdict {
  const floor = entry.lastModified - PURGE_SKEW_MS;
  let invalidated = false;
  for (const tag of tags) {
    const time = times[tag];
    if (time?.purgedAt !== undefined && time.purgedAt >= floor) return "purged";
    const expiresAt = time?.expiresAt;
    if (expiresAt !== undefined && expiresAt <= now && expiresAt > entry.lastModified) {
      return "expired";
    }
    if (time?.invalidatedAt !== undefined && time.invalidatedAt > entry.lastModified) {
      invalidated = true;
    }
  }
  const revalidate = entry.cacheControl?.revalidate ?? window?.revalidate;
  const expire = entry.cacheControl?.expire ?? window?.expire;
  const ageMs = now - entry.lastModified;
  if (typeof expire === "number" && ageMs > expire * 1000) return "expired";
  if (invalidated) return "stale";
  if (typeof revalidate === "number" && revalidate > 0 && ageMs > revalidate * 1000) return "stale";
  return "fresh";
}

function revalidatesFor(
  entry: CacheEntryFile,
  now: number,
  window: StoreWindow | undefined,
): number {
  const expire = entry.cacheControl?.expire ?? window?.expire;
  if (typeof expire !== "number") return STORE_REVALIDATES_FOR_S;
  return Math.max(1, Math.floor(expire - (now - entry.lastModified) / 1000));
}

function keptFor(entry: CacheEntryFile, now: number, window: StoreWindow | undefined): number {
  const revalidate = entry.cacheControl?.revalidate ?? window?.revalidate;
  if (typeof revalidate !== "number" || revalidate <= 0) return revalidatesFor(entry, now, window);
  return Math.max(1, Math.floor(revalidate - (now - entry.lastModified) / 1000));
}

export function storeValidator(entry: CacheEntryFile): string {
  return `W/"ocel-store-${entry.lastModified}"`;
}

function headersFrom(map: Record<string, any> | undefined): Headers {
  const headers = new Headers();
  for (const [name, value] of Object.entries(map ?? {})) {
    const lower = name.toLowerCase();
    if (lower === "x-next-cache-tags" || lower === "content-length") continue;
    headers.set(name, String(value));
  }
  return headers;
}

const REDIRECT_STATUSES = new Set([303, 307, 308]);

function build(
  call: StoreCall,
  entry: CacheEntryFile,
): { body: BodyInit; status: number; headers: Headers } | null {
  const { value } = entry;
  const restored = deserialize(value);
  let status = typeof value.status === "number" ? value.status : 200;
  const segment = segmentOf(call.headers);

  if (segment !== null) {
    const body = (restored.segmentData as Map<string, Uint8Array> | undefined)?.get(segment);
    if (!body || !value.segmentHeaders) return null;
    const headers = headersFrom(value.segmentHeaders);
    headers.set("x-nextjs-postponed", "2");
    return { body, status: 200, headers };
  }

  if (value.kind === "APP_ROUTE") {
    return {
      body: restored.body ?? new Uint8Array(),
      status,
      headers: headersFrom(value.headers),
    };
  }

  if (value.kind === "APP_PAGE" && call.headers.get("RSC") === "1") {
    if (!restored.rscData) return null;
    if (REDIRECT_STATUSES.has(status)) status = 200;
    const headers = headersFrom(value.rscHeaders);
    if (!headers.has("content-type")) headers.set("content-type", "text/x-component");
    if (call.headers.get("next-router-prefetch") === "1" && value.postponed !== undefined) {
      if (!headers.has("x-nextjs-postponed")) headers.set("x-nextjs-postponed", "1");
    }
    return { body: restored.rscData, status, headers };
  }

  const headers = headersFrom(value.headers);
  if (!headers.has("content-type")) headers.set("content-type", "text/html; charset=utf-8");
  const html = new TextEncoder().encode(value.html ?? "");
  if (
    call.variant === PPR_SHELL_VARIANT &&
    value.kind === "APP_PAGE" &&
    typeof value.postponed === "string"
  ) {
    const postponed = new TextEncoder().encode(value.postponed);
    const framed = new Uint8Array(postponed.byteLength + html.byteLength);
    framed.set(postponed, 0);
    framed.set(html, postponed.byteLength);
    headers.set(POSTPONED_LENGTH_HEADER, String(postponed.byteLength));
    headers.set("x-nextjs-postponed", "1");
    return { body: framed, status, headers };
  }
  return { body: html, status, headers };
}

export async function readFromStore(
  call: StoreCall,
  deps: StoreReadDeps,
): Promise<StoreAnswer | null> {
  const prefetch = call.headers.get("next-router-prefetch");
  if (prefetch === "2" || prefetch === "3") return null;

  const found = await findEntry(deps, call);
  if (!found) return null;
  const { entry } = found;

  const rawTags = tagsOf(entry.value, {});
  const { tags, unstorable } = storedCacheTags(call.release, rawTags);
  if (unstorable.length > 0) return null;

  const times = await deps.readTimes([call.release, ...tags]);
  const now = deps.now();
  const verdict = judge(entry, times, [call.release, ...tags], now, call.window);
  if (verdict === "purged" || verdict === "expired") return null;

  const built = build(call, entry);
  if (!built) return null;

  const stale = verdict === "stale";
  const { body, status, headers } = built;
  headers.set(
    "cache-control",
    call.sharedShell
      ? `s-maxage=${keptFor(entry, now, call.window)}`
      : `s-maxage=0, stale-while-revalidate=${revalidatesFor(entry, now, call.window)}`,
  );
  headers.set("etag", storeValidator(entry));
  headers.set(edgeCacheTagHeader, tags.join(","));
  headers.set(ENTRY_SOURCE_HEADER, stale ? "stale-store" : "store");
  return { response: new Response(body, { status, headers }), stale, from: found.from };
}
