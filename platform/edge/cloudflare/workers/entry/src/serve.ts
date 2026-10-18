import { WorkerEntrypoint } from "cloudflare:workers";
import type { ImageOriginRequest } from "@framework/next-protocol/image";
import { deltaSeconds } from "@framework/next-router/http-cache";
import {
  functionUrlImageOrigin,
  type ImageCall,
  originImagePath,
  servedImage,
} from "@framework/next-router/image";
import { asSegmentPayload, isSegmentPrefetch } from "@framework/next-router/segment";
import { storedCacheTags } from "@platform/edge-contract/cache-tags";
import { edgeCacheTagHeader } from "@platform/edge-contract/edge-cache";
import type { OriginImages } from "@platform/edge-contract/origin-reach";
import {
  type PurgeMode,
  purgeHeader,
  readPurge,
  readReleaseToken,
} from "@platform/edge-contract/purge";
import { createReleaseEdgeInvoker, ownBundleKey } from "./edge";
import type { Env } from "./env";
import { newPurgeQueuePort, openPurgeQueue } from "./exports";
import { openOrigin } from "./origin/reach";
import type { OriginObjects } from "./origin/store";
import { PPR_SHELL_VARIANT } from "./ppr";
import type { PurgeOutcome } from "./purge-backlog";
import { purgeInBand } from "./purge-inband";
import { parsePurgeOutcome } from "./purge-outcome";
import type { ReleaseScope } from "./purge-times";
import type { StoredRecord } from "./releases";
import {
  BROWSER_REVALIDATES,
  type CachedResponse,
  forCache,
  NOT_STORED,
  withholdFromEdge,
} from "./serve-headers";
import {
  buildEdgeUrl,
  buildOriginUrl,
  IMAGE_CALL_HEADER,
  type ObjectProps,
  parseKeyRelease,
  parseObjectKey,
  RENDER_RELEASE_HEADER,
  type RenderRelease,
  readEdgeEntry,
  readImageOptimizer,
  readRenderCall,
  SERVE_LOOKUP_ONLY_HEADER,
  SERVE_MISS_HEADER,
  SERVE_NO_SHELL_HEADER,
  SERVE_REFUSED_HEADER,
  SERVE_UNSHARED_HEADER,
  type ServeProps,
  type StoredEntryCall,
} from "./serve-key";
import { serveObjects } from "./serve-objects";
import { settledWithin } from "./settled";
import { readFromStore, type StoreAnswer } from "./store-read";

const OBJECT_EDGE_CACHE = "max-age=31536000";

const MISSING_EDGE_CACHE = "max-age=60";

const IMAGE_STALE_SECONDS = 86_400;

const IMAGE_PATH_TAG_PATHNAME = "/_next/image";

const DATA_REQUEST_PATH = /\/_next\/data\//;

function readImageCall(header: string | null): ImageCall | undefined {
  if (header === null) return undefined;
  try {
    const parsed = JSON.parse(header) as { payload?: Partial<ImageOriginRequest> } & {
      minimumCacheTTL?: unknown;
    };
    const { payload } = parsed;
    const text = [
      payload?.assetPrefix,
      payload?.url,
      payload?.accept,
      payload?.mimeType,
      payload?.configHash,
    ];
    if (
      !payload ||
      !text.every((value) => typeof value === "string") ||
      typeof payload.w !== "number" ||
      typeof payload.q !== "number" ||
      typeof parsed.minimumCacheTTL !== "number"
    ) {
      return undefined;
    }
    return { payload: payload as ImageOriginRequest, minimumCacheTTL: parsed.minimumCacheTTL };
  } catch {
    return undefined;
  }
}

function imageForCache(response: Response): Response {
  const headers = new Headers(response.headers);
  headers.delete("vary");
  const ttl = deltaSeconds(headers.get("cache-control"), "max-age") ?? 0;
  if (response.status === 200 && ttl > 0) {
    headers.set(
      "cdn-cache-control",
      `max-age=${ttl}, stale-while-revalidate=${IMAGE_STALE_SECONDS}`,
    );
  }
  return new Response(response.body, { status: response.status, headers });
}

export const REGENERATION_HOLD_MS = 10_000;

interface ObjectBody {
  body: ReadableStream | null;
  etag?: string;
}

function parseRenderRelease(header: string | null): RenderRelease | undefined {
  if (header === null) return undefined;
  try {
    const parsed = JSON.parse(header) as Partial<RenderRelease>;
    if (typeof parsed.slug !== "string" || typeof parsed.isrPrefix !== "string") return undefined;
    const tags = Array.isArray(parsed.tags) ? parsed.tags.filter((t) => typeof t === "string") : [];
    return {
      slug: parsed.slug,
      isrPrefix: parsed.isrPrefix,
      tags,
      ...(parsed.sharesFallbackShell === true && { sharesFallbackShell: true }),
      ...(parsed.honoursAppCacheHeaders === true && { honoursAppCacheHeaders: true }),
      entry: readStoredEntryCall(parsed.entry),
    };
  } catch {
    return undefined;
  }
}

function readStoredEntryCall(entry: unknown): StoredEntryCall | undefined {
  const call = entry as Partial<StoredEntryCall> | null | undefined;
  if (typeof call?.path !== "string" || !call.path.startsWith("/")) return undefined;
  const count = (value: unknown) => (typeof value === "number" && value >= 0 ? value : undefined);
  return {
    path: call.path,
    fallbackPath: typeof call.fallbackPath === "string" ? call.fallbackPath : undefined,
    revalidate: count(call.revalidate),
    expire: count(call.expire),
    ...(call.pathDeterminesQuery === true && { pathDeterminesQuery: true }),
  };
}

function isKeyedByPath(render: RenderRelease, keyedUrl: string): boolean {
  return render.entry?.pathDeterminesQuery === true || new URL(keyedUrl).search === "";
}

function isRevalidation(request: Request): boolean {
  return request.headers.has("if-none-match") || request.headers.has("if-modified-since");
}

function admitSegment(request: Request, response: Response): Response {
  return isSegmentPrefetch(request.headers) ? asSegmentPayload(response) : response;
}

function respondNotStored(status: number, message: string): Response {
  return new Response(message, { status, headers: { "cache-control": NOT_STORED } });
}

const UNSHARED_KEPT = "s-maxage=31536000";

function respondUnshared(kept: boolean, cached: CachedResponse): Response {
  const unshared = new Response(null, {
    status: 404,
    headers: {
      "cache-control": kept ? UNSHARED_KEPT : NOT_STORED,
      [SERVE_UNSHARED_HEADER]: "1",
    },
  });
  return kept ? forCache(unshared, cached) : unshared;
}

function respondNoShell(): Response {
  return new Response(null, {
    status: 404,
    headers: { "cache-control": NOT_STORED, [SERVE_NO_SHELL_HEADER]: "1" },
  });
}

const SERVE_MARKER_HEADERS = [
  SERVE_MISS_HEADER,
  SERVE_REFUSED_HEADER,
  SERVE_NO_SHELL_HEADER,
  SERVE_UNSHARED_HEADER,
];

function withoutServeMarkers(response: Response): Response {
  if (!SERVE_MARKER_HEADERS.some((name) => response.headers.has(name))) return response;
  const headers = new Headers(response.headers);
  for (const name of SERVE_MARKER_HEADERS) headers.delete(name);
  return new Response(response.body, { status: response.status, headers });
}

function respondMiss(): Response {
  return new Response(null, {
    status: 404,
    headers: { "cache-control": NOT_STORED, [SERVE_MISS_HEADER]: "1" },
  });
}

function respondRefused(message: string): Response {
  return new Response(message, {
    status: 503,
    headers: { "cache-control": NOT_STORED, [SERVE_REFUSED_HEADER]: "1" },
  });
}

async function readRenderRecord(
  env: Env,
  request: Request,
  release: string,
): Promise<{ record: StoredRecord; render: RenderRelease } | undefined> {
  const render = parseRenderRelease(request.headers.get(RENDER_RELEASE_HEADER));
  if (!render) return undefined;
  const served = await env.RELEASES.readServedRecord({
    slug: render.slug,
    isrPrefix: render.isrPrefix,
  });
  if (served.kind !== "record" || readReleaseToken(served.record.isrPrefix) !== release) {
    return undefined;
  }
  return { record: served.record, render };
}

function isReleaseImageOptimizer(
  images: OriginImages | undefined,
  record: StoredRecord,
  optimizerUrl: string,
): boolean {
  if (images?.optimizerUrl !== undefined && optimizerUrl === images.optimizerUrl) return true;
  try {
    const url = new URL(optimizerUrl);
    return url.pathname === originImagePath && isReleaseOrigin(record, url.origin);
  } catch {
    return false;
  }
}

function isReleaseOrigin(record: StoredRecord, origin: string): boolean {
  return Object.values(record.functionUrls).some((functionUrl) => {
    try {
      return new URL(functionUrl).origin === origin;
    } catch {
      return false;
    }
  });
}

function respondStored(object: ObjectBody, release: string): Response {
  const headers = new Headers({
    "cloudflare-cdn-cache-control": OBJECT_EDGE_CACHE,
    "cache-control": BROWSER_REVALIDATES,
    "cache-tag": release,
  });
  if (object.etag) headers.set("etag", object.etag);
  return new Response(object.body, { status: 200, headers });
}

function respondMissing(release: string): Response {
  return new Response("Not Found", {
    status: 404,
    headers: {
      "cloudflare-cdn-cache-control": MISSING_EDGE_CACHE,
      "cache-control": NOT_STORED,
      "cache-tag": release,
    },
  });
}

async function readStoredObject(
  objects: OriginObjects,
  key: string,
  release: string,
): Promise<Response> {
  const res = await objects.readObject(key);
  if (res.ok) {
    return respondStored({ body: res.body, etag: res.headers.get("etag") ?? undefined }, release);
  }
  await res.body?.cancel();
  if (res.status === 404) return respondMissing(release);
  console.error(`ocel: the origin's store answered ${res.status} for ${objects.objectUrl(key)}`);
  return respondNotStored(502, "Bad Gateway");
}

type PurgeableCache = CacheContext & {
  invalidate(options: { tags: string[] }): Promise<CachePurgeResult>;
};

export class Serve extends WorkerEntrypoint<Env, ServeProps> {
  async fetch(request: Request): Promise<Response> {
    const props = this.ctx.props;
    if (request.method !== "GET" && request.method !== "HEAD") {
      return respondNotStored(405, "Method Not Allowed");
    }
    if (props?.kind === "origin") {
      const optimizer = readImageOptimizer(request.url);
      if (optimizer !== undefined) return this.fromImage(request, optimizer);
      const entryKey = readEdgeEntry(request.url);
      return entryKey === undefined ? this.fromOrigin(request) : this.fromEdge(request, entryKey);
    }
    if (!props?.host || !props.app || !props.release) {
      throw new Error("ocel: Serve was called with no host, app and release to key its cache by");
    }
    return this.fromObjects(request, props);
  }

  private async fromObjects(request: Request, props: ObjectProps): Promise<Response> {
    const key = parseObjectKey(request.url);
    const { app, release } = parseKeyRelease(key);
    if (app !== props.app) throw new Error(`ocel: ${key} is not an object of app ${props.app}`);
    if (release !== props.release) {
      throw new Error(`ocel: ${key} is not an object of release ${props.release}`);
    }
    const { objects } = openOrigin(this.env);
    if (objects) return readStoredObject(objects, key, release);
    return respondNotStored(503, "no store to read from");
  }

  private async fromImage(request: Request, optimizerUrl: string): Promise<Response> {
    const { release } = readRenderCall(request.url);
    const origin = openOrigin(this.env);
    const found = await readRenderRecord(this.env, request, release);
    if (!found || !isReleaseImageOptimizer(origin.images, found.record, optimizerUrl)) {
      return respondRefused("the image optimizer is not one of the served release's");
    }
    const call = readImageCall(request.headers.get(IMAGE_CALL_HEADER));
    const optimize = functionUrlImageOrigin(optimizerUrl, origin.fetch);
    if (!call || !optimize) return respondRefused("there is no image to optimize");
    const optimized = servedImage(
      withoutServeMarkers(await optimize(call.payload)),
      call.minimumCacheTTL,
    );
    return forCache(imageForCache(optimized), { release, pathname: IMAGE_PATH_TAG_PATHNAME });
  }

  private async fromOrigin(request: Request): Promise<Response> {
    const { host, release, variant } = readRenderCall(request.url);
    const keyedUrl = buildOriginUrl(request.url);
    const found = await readRenderRecord(this.env, request, release);
    if (!found || !isReleaseOrigin(found.record, new URL(keyedUrl).origin)) {
      return respondRefused("the origin is not one of the served release's functions");
    }
    const cached = { release, pathname: new URL(keyedUrl).pathname };
    if (found.render.sharesFallbackShell) {
      return this.fromFallbackShell(request, found.render, release, cached);
    }
    const honoursAppCacheHeaders = found.render.honoursAppCacheHeaders === true;
    if (
      honoursAppCacheHeaders &&
      request.headers.has(SERVE_LOOKUP_ONLY_HEADER) &&
      !isRevalidation(request)
    ) {
      return respondMiss();
    }
    const scope = { slug: found.render.slug, isrPrefix: found.render.isrPrefix };
    const render = () =>
      this.renderAtOrigin(request, { host, release, scope, keyedUrl, honoursAppCacheHeaders });
    if (variant === PPR_SHELL_VARIANT) {
      const regenerate = () =>
        this.renderAtOrigin(request, { host, release, scope, keyedUrl, unconditional: true });
      return this.fromStoredShell(
        request,
        { render: regenerate, release, keyedUrl, cached },
        found.render,
      );
    }
    if (isRevalidation(request) || !isKeyedByPath(found.render, keyedUrl)) return render();
    let stored: StoreAnswer | null = null;
    try {
      stored = await this.askStore(request, found.render, release);
    } catch (error) {
      console.error("ocel: the durable store was not read; rendering at the origin", error);
    }
    if (!stored) return render();
    if (stored.from === "lazy-fallback") return withholdFromEdge(stored.response);
    return forCache(admitSegment(request, stored.response), cached);
  }

  private async fromStoredShell(
    request: Request,
    call: {
      render: () => Promise<Response>;
      release: string;
      keyedUrl: string;
      cached: CachedResponse;
    },
    render: RenderRelease,
  ): Promise<Response> {
    let shell: StoreAnswer | null = null;
    try {
      if (isKeyedByPath(render, call.keyedUrl)) {
        shell = await this.askStore(request, render, call.release);
      }
    } catch (error) {
      console.error("ocel: the durable store was not read for a PPR shell", error);
    }
    if (!shell) return respondNoShell();
    if ((shell.stale || shell.from !== "own") && isRevalidation(request)) {
      const regenerating = call
        .render()
        .then((regenerated) => regenerated.body?.pipeTo(new WritableStream()))
        .catch((error) => console.error("ocel: the origin did not regenerate a PPR page", error));
      this.ctx.waitUntil(regenerating);
      await settledWithin(regenerating, REGENERATION_HOLD_MS);
    }
    return forCache(admitSegment(request, shell.response), call.cached);
  }

  private async fromFallbackShell(
    request: Request,
    render: RenderRelease,
    release: string,
    cached: CachedResponse,
  ): Promise<Response> {
    let shell: StoreAnswer | null;
    try {
      shell = await this.askStore(request, render, release, true);
    } catch (error) {
      console.error("ocel: the durable store was not read for a fallback shell", error);
      return respondUnshared(false, cached);
    }
    if (!shell || shell.stale) return respondUnshared(true, cached);
    return forCache(admitSegment(request, shell.response), cached);
  }

  private async askStore(
    request: Request,
    render: RenderRelease,
    release: string,
    sharedShell = false,
  ): Promise<StoreAnswer | null> {
    const { entry } = render;
    if (!entry || request.method !== "GET") return null;
    const source = openOrigin(this.env).entries;
    if (!source) return null;
    return readFromStore(
      {
        isrPrefix: render.isrPrefix,
        release,
        entryPath: entry.path,
        fallbackPath: sharedShell ? undefined : entry.fallbackPath,
        window: { revalidate: entry.revalidate, expire: entry.expire },
        variant: readRenderCall(request.url).variant,
        headers: request.headers,
        sharedShell,
        documentRequest: !DATA_REQUEST_PATH.test(new URL(buildOriginUrl(request.url)).pathname),
      },
      {
        source,
        readTimes: (tags) => openPurgeQueue(this.ctx.exports, render.slug).readTimes(tags),
        now: Date.now,
      },
    );
  }

  private async renderAtOrigin(
    request: Request,
    call: {
      host: string;
      release: string;
      scope: ReleaseScope;
      keyedUrl: string;
      unconditional?: boolean;
      honoursAppCacheHeaders?: boolean;
    },
  ): Promise<Response> {
    const { host, release, keyedUrl } = call;
    const headers = new Headers(request.headers);
    headers.delete(RENDER_RELEASE_HEADER);
    if (call.unconditional) {
      headers.delete("if-none-match");
      headers.delete("if-modified-since");
    }
    const originFetch = openOrigin(this.env).fetch;
    headers.set("x-forwarded-host", host);
    headers.set("x-forwarded-proto", "https");
    const res = await originFetch(
      new Request(keyedUrl, {
        method: request.method,
        headers,
        redirect: "manual",
      }),
    );
    const announced = res.headers.get(purgeHeader);
    if (announced !== null) {
      this.ctx.waitUntil(
        purgeInBand(readPurge(announced), {
          queue: newPurgeQueuePort(this.ctx.exports),
          scope: call.scope,
          now: Date.now,
        }),
      );
    }
    return forCache(
      admitSegment(request, withoutServeMarkers(res)),
      { release, pathname: new URL(keyedUrl).pathname },
      { honoursAppCacheHeaders: call.honoursAppCacheHeaders === true },
    );
  }

  private async fromEdge(request: Request, entryKey: string): Promise<Response> {
    const { host, app, release } = readRenderCall(request.url);
    const found = await readRenderRecord(this.env, request, release);
    if (!found) return respondRefused("the release is not served");
    const { record, render: edge } = found;
    const { code } = record.edge;
    if (!code || !ownBundleKey(code.bundleKey, edge.slug, app)) {
      return respondRefused("the release has no edge bundle of its own to render with");
    }
    if (!openOrigin(this.env).objects) {
      return respondRefused("no store to read the edge bundle from");
    }
    const invoke = createReleaseEdgeInvoker(
      { loader: this.env.LOADER, envelopeKey: this.env.OCEL_ENVELOPE_KEY },
      serveObjects(this.ctx.exports.Serve, host),
      record,
      code,
    );
    const headers = new Headers(request.headers);
    headers.delete(RENDER_RELEASE_HEADER);
    const publicUrl = buildEdgeUrl(request.url);
    const rendered = await invoke(
      entryKey,
      new Request(publicUrl, { method: request.method, headers, redirect: "manual" }),
    );
    const taggedHeaders = new Headers(rendered.headers);
    for (const name of SERVE_MARKER_HEADERS) taggedHeaders.delete(name);
    const routeTags = storedCacheTags(release, edge.tags).tags;
    if (routeTags.length > 0) taggedHeaders.set(edgeCacheTagHeader, routeTags.join(","));
    else taggedHeaders.delete(edgeCacheTagHeader);
    const tagged = new Response(rendered.body, { status: rendered.status, headers: taggedHeaders });
    return forCache(admitSegment(request, tagged), {
      release,
      pathname: new URL(publicUrl).pathname,
    });
  }

  async purge(mode: PurgeMode, tags: string[]): Promise<PurgeOutcome> {
    return this.clear((cache) =>
      mode === "purge" ? cache.purge({ tags }) : cache.invalidate({ tags }),
    );
  }

  private async clear(
    run: (cache: PurgeableCache) => Promise<CachePurgeResult>,
  ): Promise<PurgeOutcome> {
    const cache = this.ctx.cache as PurgeableCache | undefined;
    if (!cache) return { ok: false, codes: [], message: "this worker has no cache to purge" };
    try {
      return parsePurgeOutcome(await run(cache));
    } catch (error) {
      return parsePurgeOutcome(error);
    }
  }
}
