import { NEXT_CACHE_STATUS, withStatus } from "@framework/next-router/http-cache";
import {
  type ImageCache,
  type ImageCacheContext,
  type ImageCall,
  serveUncachedImage,
  withServedCacheControl,
} from "@framework/next-router/image";
import {
  buildImageCall,
  buildServeRequest,
  IMAGE_CALL_HEADER,
  RENDER_RELEASE_HEADER,
  type RenderRelease,
  SERVE_REFUSED_HEADER,
} from "./serve-key";
import { readTier, type Serving } from "./serve-prerender";
import { ENTRY_SOURCE_HEADER } from "./store-read";

export interface WorkersCacheImageDeps {
  serving: Serving;
  app: string;
  optimizerUrl: string;
}

function buildVisitorResponse(response: Response, context: ImageCacheContext): Response {
  const tier = readTier(response);
  const headers = new Headers(response.headers);
  for (const name of [
    "cache-tag",
    "cloudflare-cdn-cache-control",
    "cf-cache-status",
    ENTRY_SOURCE_HEADER,
  ]) {
    headers.delete(name);
  }
  if (response.status === 200) headers.set("vary", "accept");
  if (headers.has(NEXT_CACHE_STATUS) && tier !== "MISS") headers.set(NEXT_CACHE_STATUS, tier);
  const visitor = new Response(response.body, { status: response.status, headers });
  return withServedCacheControl(withStatus(visitor, tier), context);
}

function asciiJson(value: unknown): string {
  return JSON.stringify(value).replace(
    /[\u007f-\uffff]/g,
    (char) => `\\u${char.charCodeAt(0).toString(16).padStart(4, "0")}`,
  );
}

export function workersCacheImage(deps: WorkersCacheImageDeps): ImageCache {
  const { serving, app, optimizerUrl } = deps;
  return async (context) => {
    const { method } = context.request;
    if (method !== "GET" && method !== "HEAD") return serveUncachedImage(context);

    const { url, props } = buildImageCall(
      optimizerUrl,
      { host: serving.host, app, release: serving.release },
      context.digest,
    );
    const served = buildServeRequest(url);
    const release: RenderRelease = {
      slug: serving.slug,
      storageCoordinate: serving.storageCoordinate,
      tags: [],
    };
    served.headers.set(RENDER_RELEASE_HEADER, JSON.stringify(release));
    const call: ImageCall = { payload: context.payload, minimumCacheTTL: context.minimumCacheTTL };
    served.headers.set(IMAGE_CALL_HEADER, asciiJson(call));

    const response = await serving.serve({ props }).fetch(served);
    if (response.headers.has(SERVE_REFUSED_HEADER)) {
      await response.body?.cancel();
      return serveUncachedImage(context);
    }
    return buildVisitorResponse(response, context);
  };
}
