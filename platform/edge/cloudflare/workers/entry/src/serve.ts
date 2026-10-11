import { WorkerEntrypoint } from "cloudflare:workers";
import { storedCacheTags } from "@framework/next-cache";
import { asSegmentPayload, isSegmentPrefetch } from "@framework/next-router/segment";
import { edgeCacheTagHeader } from "@platform/edge-contract/edge-cache";
import {
  type PurgeMode,
  purgeHeader,
  readPurge,
  readReleaseToken,
} from "@platform/edge-contract/purge";
import { type BucketAccess, findBucketAccess, signBucket } from "./bucket-access";
import { createReleaseEdgeInvoker, type EdgeRuntime, ownBundleKey } from "./edge";
import type { Env } from "./env";
import { originFetchFor } from "./origin-fetch";
import type { PurgeOutcome } from "./purge-backlog";
import { purgeInBand } from "./purge-inband";
import { parsePurgeOutcome } from "./purge-outcome";
import { openInBandQueue, openPurgeQueue } from "./purge-request";
import type { ReleaseRecord } from "./releases";
import { BROWSER_REVALIDATES, type CachedResponse, forCache, NOT_STORED } from "./serve-headers";
import {
  buildEdgeUrl,
  buildOriginUrl,
  encodeKeyPath,
  type ObjectProps,
  parseKeyRelease,
  parseObjectKey,
  RENDER_RELEASE_HEADER,
  type RenderRelease,
  readEdgeEntry,
  readRenderCall,
  SERVE_REFUSED_HEADER,
  SERVE_UNSHARED_HEADER,
  type ServeProps,
  type StoredEntryCall,
} from "./serve-key";
import { type ServeFactory, serveObjects } from "./serve-objects";
import { readFromStore, type StoreAnswer } from "./store-read";
import { openEntrySource } from "./store-source";

const OBJECT_EDGE_CACHE = "max-age=31536000";

const MISSING_EDGE_CACHE = "max-age=60";

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
  };
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
): Promise<{ record: ReleaseRecord; render: RenderRelease } | undefined> {
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

function isReleaseOrigin(record: ReleaseRecord, origin: string): boolean {
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

export function readsObjects(env: Env): boolean {
  return findBucketAccess(env) !== undefined || env.OCEL_CACHE_STORE !== undefined;
}

async function readBucket(access: BucketAccess, key: string, release: string): Promise<Response> {
  const { bucket, origin, prefix } = access;
  const res = await signBucket(access).fetch(`${origin}/${encodeKeyPath(prefix + key)}`);
  if (res.ok) {
    return respondStored({ body: res.body, etag: res.headers.get("etag") ?? undefined }, release);
  }
  await res.body?.cancel();
  if (res.status === 404) return respondMissing(release);
  console.error(`ocel: the bucket ${bucket} answered ${res.status} for ${prefix}${key}`);
  return respondNotStored(502, "Bad Gateway");
}

async function readStore(store: R2Bucket, key: string, release: string): Promise<Response> {
  const object = await store.get(key);
  if (!object) return respondMissing(release);
  return respondStored({ body: object.body, etag: object.httpEtag }, release);
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
    const access = findBucketAccess(this.env);
    if (access) return readBucket(access, key, release);
    const store = this.env.OCEL_CACHE_STORE;
    if (store) return readStore(store, key, release);
    return respondNotStored(503, "no store to read from");
  }

  private async fromOrigin(request: Request): Promise<Response> {
    const { host, release } = readRenderCall(request.url);
    const keyedUrl = buildOriginUrl(request.url);
    const found = await readRenderRecord(this.env, request, release);
    if (!found || !isReleaseOrigin(found.record, new URL(keyedUrl).origin)) {
      return respondRefused("the origin is not one of the served release's functions");
    }
    const cached = { release, pathname: new URL(keyedUrl).pathname };
    if (found.render.sharesFallbackShell) {
      return this.fromFallbackShell(request, found.render, release, cached);
    }
    const render = () => this.renderAtOrigin(request, { host, release, keyedUrl });
    if (isRevalidation(request) || new URL(keyedUrl).search !== "") return render();
    let stored: StoreAnswer | null = null;
    try {
      stored = await this.askStore(request, found.render, release);
    } catch (error) {
      console.error("ocel: the durable store was not read; rendering at the origin", error);
    }
    if (!stored) return render();
    return forCache(admitSegment(request, stored.response), cached);
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
    const source = openEntrySource(this.env);
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
      },
      {
        source,
        readTimes: (tags) => openPurgeQueue(this.ctx.exports).readTimes(tags),
        now: Date.now,
      },
    );
  }

  private async renderAtOrigin(
    request: Request,
    call: { host: string; release: string; keyedUrl: string },
  ): Promise<Response> {
    const { host, release, keyedUrl } = call;
    const headers = new Headers(request.headers);
    headers.delete(RENDER_RELEASE_HEADER);
    const originFetch = originFetchFor(this.env) ?? fetch;
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
          cache: this,
          queue: openInBandQueue(this.ctx.exports),
          times: openPurgeQueue(this.ctx.exports),
          now: Date.now,
        }),
      );
    }
    return forCache(admitSegment(request, res), { release, pathname: new URL(keyedUrl).pathname });
  }

  private async fromEdge(request: Request, entryKey: string): Promise<Response> {
    const { host, app, release } = readRenderCall(request.url);
    const { LOADER: loader } = this.env;
    if (!loader) return respondRefused("no edge runtime to render with");
    const found = await readRenderRecord(this.env, request, release);
    if (!found) return respondRefused("the release is not served");
    const { record, render: edge } = found;
    const { edgeWorkers } = record;
    if (!edgeWorkers || !ownBundleKey(edgeWorkers.bundleKey, edge.slug, app)) {
      return respondRefused("the release has no edge bundle of its own to render with");
    }
    const exports = this.ctx.exports as {
      Serve?: ServeFactory;
      CacheEntrypoint?: EdgeRuntime["cacheEntrypoint"];
    };
    const store =
      exports.Serve && readsObjects(this.env)
        ? serveObjects(exports.Serve, host)
        : this.env.OCEL_CACHE_STORE;
    if (!store) return respondRefused("no store to read the edge bundle from");
    const invoke = createReleaseEdgeInvoker(
      {
        loader,
        store,
        cacheEntrypoint: exports.CacheEntrypoint,
        envelopeKey: this.env.OCEL_ENVELOPE_KEY,
      },
      { ...record, edgeWorkers },
    );
    const headers = new Headers(request.headers);
    headers.delete(RENDER_RELEASE_HEADER);
    const publicUrl = buildEdgeUrl(request.url);
    const rendered = await invoke(
      entryKey,
      new Request(publicUrl, { method: request.method, headers, redirect: "manual" }),
    );
    const taggedHeaders = new Headers(rendered.headers);
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
