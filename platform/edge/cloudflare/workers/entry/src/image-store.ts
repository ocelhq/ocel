import { NEXT_CACHE_STATUS } from "@framework/next-router/http-cache";

import {
  imageStorable,
  refreshOnce,
  servedFromStore,
  type CacheDeps,
} from "./cache";

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

const ENTRY_VERSION = "ocel-image-version";
const ENTRY_FORMAT = "1";
const ENTRY_HEADERS = "ocel-image-headers";

const STORED_HEADERS = [
  "content-type",
  "cache-control",
  "etag",
  "content-disposition",
  "content-security-policy",
  "vary",
  NEXT_CACHE_STATUS,
];

const METADATA_BUDGET = 2048;

export function imageObjectKey(slug: string, digest: string): string {
  return `images/${slug}/${digest}`;
}

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

function optimizeAndStore(
  store: ImageStore,
  cache: CacheDeps,
  key: string,
  origin: () => Promise<Response>,
): () => Promise<Response> {
  return async () => {
    const optimized = await origin();
    if (optimized.status === 200 && imageStorable(optimized)) {
      const copy = optimized.clone();
      refreshOnce(cache, key, () =>
        writeImage(store, key, copy).catch((error: unknown) => {
          console.warn(`ocel: could not store the optimized image ${key}`, error);
        }),
      );
    }
    return optimized;
  };
}

export function durableImageOrigin(
  store: ImageStore,
  cache: CacheDeps,
  key: string,
  origin: () => Promise<Response>,
): () => Promise<Response> {
  const write = optimizeAndStore(store, cache, key, origin);
  return async () => (await readImage(store, key)) ?? write();
}

export const durableImageRefresh = optimizeAndStore;
