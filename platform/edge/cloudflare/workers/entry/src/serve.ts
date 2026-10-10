import { WorkerEntrypoint } from "cloudflare:workers";
import { AwsClient } from "aws4fetch";
import type { Env } from "./env";
import { originFetchFor } from "./origin-fetch";
import { forCache } from "./serve-headers";
import { objectKeyOf, releaseOfKey, type ServeProps } from "./serve-key";

const OBJECT_EDGE_CACHE = "max-age=31536000";

const MISSING_EDGE_CACHE = "max-age=60";

const BROWSER_REVALIDATES = "public, max-age=0, must-revalidate";

const NOT_STORED = "private, no-store";

interface ObjectBody {
  body: ReadableStream | null;
  etag?: string;
}

function noStore(status: number, message: string): Response {
  return new Response(message, { status, headers: { "cache-control": NOT_STORED } });
}

function stored(object: ObjectBody, release: string): Response {
  const headers = new Headers({
    "cloudflare-cdn-cache-control": OBJECT_EDGE_CACHE,
    "cache-control": BROWSER_REVALIDATES,
    "cache-tag": release,
  });
  if (object.etag) headers.set("etag", object.etag);
  return new Response(object.body, { status: 200, headers });
}

function missing(release: string): Response {
  return new Response("Not Found", {
    status: 404,
    headers: {
      "cloudflare-cdn-cache-control": MISSING_EDGE_CACHE,
      "cache-control": NOT_STORED,
      "cache-tag": release,
    },
  });
}

async function readBucket(env: Env, key: string, release: string): Promise<Response> {
  const { OCEL_ASSET_BUCKET: bucket, OCEL_AWS_REGION: region } = env;
  const client = new AwsClient({
    accessKeyId: env.OCEL_EDGE_ACCESS_KEY_ID ?? "",
    secretAccessKey: env.OCEL_EDGE_SECRET_KEY ?? "",
    service: "s3",
    region,
    retries: 0,
  });
  const path = key.split("/").map(encodeURIComponent).join("/");
  const res = await client.fetch(`https://${bucket}.s3.${region}.amazonaws.com/${path}`);
  if (res.ok)
    return stored({ body: res.body, etag: res.headers.get("etag") ?? undefined }, release);
  await res.body?.cancel();
  if (res.status === 404 || res.status === 403) return missing(release);
  console.error(`ocel: the bucket ${bucket} answered ${res.status} for ${key}`);
  return noStore(502, "Bad Gateway");
}

async function readStore(store: R2Bucket, key: string, release: string): Promise<Response> {
  const object = await store.get(key);
  if (!object) return missing(release);
  return stored({ body: object.body, etag: object.httpEtag }, release);
}

export class Serve extends WorkerEntrypoint<Env, ServeProps> {
  async fetch(request: Request): Promise<Response> {
    const props = this.ctx.props;
    if (!props?.host || !props.app || !props.release) {
      throw new Error("ocel: Serve was called with no host, app and release to key its cache by");
    }
    if (request.method !== "GET" && request.method !== "HEAD") {
      return noStore(405, "Method Not Allowed");
    }
    if (props.kind === "origin") return this.fromOrigin(request, props);
    return this.fromObjects(request, props);
  }

  private async fromObjects(request: Request, props: ServeProps): Promise<Response> {
    const key = objectKeyOf(request.url);
    const { app, release } = releaseOfKey(key);
    if (app !== props.app) throw new Error(`ocel: ${key} is not an object of app ${props.app}`);
    if (release !== props.release) {
      throw new Error(`ocel: ${key} is not an object of release ${props.release}`);
    }
    const {
      OCEL_ASSET_BUCKET: bucket,
      OCEL_AWS_REGION: region,
      OCEL_CACHE_STORE: store,
    } = this.env;
    if (bucket && region) {
      const res = await readBucket(this.env, key, release);
      if (res.status !== 404 || !store) return res;
    }
    if (store) return readStore(store, key, release);
    return noStore(503, "no store to read from");
  }

  private async fromOrigin(request: Request, props: ServeProps): Promise<Response> {
    const originFetch = originFetchFor(this.env) ?? fetch;
    const res = await originFetch(request);
    return forCache(res, { release: props.release, pathname: new URL(request.url).pathname });
  }
}
