import { WorkerEntrypoint } from "cloudflare:workers";
import { storedCacheTags } from "@framework/next-cache";
import {
  type PurgeMode,
  purgeHeader,
  readPurge,
  readReleaseToken,
} from "@platform/edge-contract/purge";
import { AwsClient } from "aws4fetch";
import { createReleaseEdgeInvoker, type EdgeRuntime, ownBundleKey } from "./edge";
import type { Env } from "./env";
import { originFetchFor } from "./origin-fetch";
import type { PurgeOutcome } from "./purge-backlog";
import { purgeInBand } from "./purge-inband";
import { parsePurgeOutcome } from "./purge-outcome";
import { openInBandQueue } from "./purge-request";
import type { ReleaseRecord } from "./releases";
import { BROWSER_REVALIDATES, forCache, NOT_STORED } from "./serve-headers";
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
  type ServeProps,
} from "./serve-key";
import { type ServeFactory, serveObjects } from "./serve-objects";

const OBJECT_EDGE_CACHE = "max-age=31536000";

const MISSING_EDGE_CACHE = "max-age=60";

interface ObjectBody {
  body: ReadableStream | null;
  etag?: string;
}

interface BucketAccess {
  bucket: string;
  origin: string;
  prefix: string;
  region: string;
  accessKeyId: string;
  secretAccessKey: string;
}

function parseRenderRelease(header: string | null): RenderRelease | undefined {
  if (header === null) return undefined;
  try {
    const parsed = JSON.parse(header) as Partial<RenderRelease>;
    if (typeof parsed.slug !== "string" || typeof parsed.isrPrefix !== "string") return undefined;
    const tags = Array.isArray(parsed.tags) ? parsed.tags.filter((t) => typeof t === "string") : [];
    return { slug: parsed.slug, isrPrefix: parsed.isrPrefix, tags };
  } catch {
    return undefined;
  }
}

function respondNotStored(status: number, message: string): Response {
  return new Response(message, { status, headers: { "cache-control": NOT_STORED } });
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

function findAwsBucketAccess(env: Env): BucketAccess | undefined {
  const {
    OCEL_ASSET_BUCKET: bucket,
    OCEL_AWS_REGION: region,
    OCEL_EDGE_ACCESS_KEY_ID: accessKeyId,
    OCEL_EDGE_SECRET_KEY: secretAccessKey,
  } = env;
  if (!bucket || !region || !accessKeyId || !secretAccessKey) return undefined;
  const origin = `https://${bucket}.s3.${region}.amazonaws.com`;
  return { bucket, origin, prefix: "", region, accessKeyId, secretAccessKey };
}

function findHmacBucketAccess(env: Env): BucketAccess | undefined {
  const {
    OCEL_ASSET_STORE_ENDPOINT: endpoint,
    OCEL_ASSET_STORE_BUCKET: bucket,
    OCEL_ASSET_STORE_ACCESS_KEY_ID: accessKeyId,
    OCEL_ASSET_STORE_SECRET_KEY: secretAccessKey,
  } = env;
  if (!endpoint || !bucket || !accessKeyId || !secretAccessKey) return undefined;
  const origin = `${endpoint.replace(/\/$/, "")}/${encodeURIComponent(bucket)}`;
  const prefix = env.OCEL_ASSET_STORE_PREFIX ?? "";
  return { bucket, origin, prefix, region: "auto", accessKeyId, secretAccessKey };
}

function findBucketAccess(env: Env): BucketAccess | undefined {
  return findAwsBucketAccess(env) ?? findHmacBucketAccess(env);
}

export function readsObjects(env: Env): boolean {
  return findBucketAccess(env) !== undefined || env.OCEL_CACHE_STORE !== undefined;
}

async function readBucket(access: BucketAccess, key: string, release: string): Promise<Response> {
  const { bucket, origin, prefix, region, accessKeyId, secretAccessKey } = access;
  const client = new AwsClient({ accessKeyId, secretAccessKey, service: "s3", region, retries: 0 });
  const res = await client.fetch(`${origin}/${encodeKeyPath(prefix + key)}`);
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
    const originUrl = buildOriginUrl(request.url);
    const found = await readRenderRecord(this.env, request, release);
    if (!found || !isReleaseOrigin(found.record, new URL(originUrl).origin)) {
      return respondRefused("the origin is not one of the served release's functions");
    }
    const headers = new Headers(request.headers);
    headers.delete(RENDER_RELEASE_HEADER);
    const originFetch = originFetchFor(this.env) ?? fetch;
    headers.set("x-forwarded-host", host);
    headers.set("x-forwarded-proto", "https");
    const res = await originFetch(
      new Request(originUrl, {
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
          now: Date.now,
        }),
      );
    }
    return forCache(res, { release, pathname: new URL(originUrl).pathname });
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
    const tagged = new Headers(rendered.headers);
    const routeTags = storedCacheTags(release, edge.tags).tags;
    if (routeTags.length > 0) tagged.set("cache-tag", routeTags.join(","));
    else tagged.delete("cache-tag");
    return forCache(new Response(rendered.body, { status: rendered.status, headers: tagged }), {
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
