import { createHash } from "node:crypto";
import {
  IMAGE_PASSTHROUGH,
  type CompiledImageConfig,
  type ImageOriginRequest,
  type OriginResponse,
} from "./contract.mjs";
import { loadImageConfig } from "./config.mjs";
import { ImageError, BOOTSTRAP_MESSAGE, upstreamFailure } from "./errors.mjs";
import { assetKey, releaseAssetPrefix } from "./keys.mjs";
import { extensionFor } from "./sniff.mjs";
import type { ObjectStore } from "./store.mjs";
import { transform, type Transformed } from "./transform.mjs";
import { fetchUpstream, type UpstreamDeps, type UpstreamImage } from "./upstream.mjs";
import { validate } from "./validate.mjs";

export interface OptimizeDeps {
  store: ObjectStore;
  upstream?: UpstreamDeps;
}

export async function optimize(
  payload: ImageOriginRequest,
  deps: OptimizeDeps,
): Promise<OriginResponse> {
  try {
    return await run(payload, deps);
  } catch (error) {
    if (error instanceof ImageError) {
      if (error.detail !== undefined) {
        console.error(`ocel image optimizer: ${error.message}`, error.detail);
      }
      return { status: error.status, headers: {}, body: encode(error.message) };
    }
    console.error("ocel image optimizer: bootstrap failure", error);
    return { status: 502, headers: {}, body: encode(BOOTSTRAP_MESSAGE) };
  }
}

async function run(
  payload: ImageOriginRequest,
  deps: OptimizeDeps,
): Promise<OriginResponse> {
  const assetPrefix = releaseAssetPrefix(payload.assetPrefix);
  const config = await loadImageConfig(deps.store, assetPrefix, payload.configHash);

  const result = validate(payload, config);
  if (!result.ok) throw new ImageError(result.status, result.message);
  const { href, isAbsolute } = result.params;

  const source = isAbsolute
    ? await fetchUpstream(href, config, deps.upstream)
    : await readLocal(deps.store, assetPrefix, href, config);

  const transformed = await transform({
    bytes: source.bytes,
    mimeType: payload.mimeType,
    width: result.params.width,
    quality: result.params.quality,
    config,
  });

  return respond(href, source, transformed, config);
}

async function readLocal(
  store: ObjectStore,
  assetPrefix: string,
  href: string,
  config: CompiledImageConfig,
): Promise<UpstreamImage> {
  const url = new URL(href, "http://n");
  const key = assetKey(assetPrefix, url.pathname);
  let object;
  try {
    object = await store.get(key, config.maximumResponseBody);
  } catch (error) {
    throw upstreamFailure(error);
  }
  if (!object) throw upstreamFailure(`no object at ${key}`);
  return object;
}

function respond(
  href: string,
  source: UpstreamImage,
  transformed: Transformed,
  config: CompiledImageConfig,
): OriginResponse {
  const headers: Record<string, string> = {
    "content-type": transformed.contentType,
    ...(source.cacheControl ? { "cache-control": source.cacheControl } : {}),
    etag: transformed.unmodified
      ? upstreamEtag(source)
      : contentEtag(transformed.bytes),
    "content-disposition": contentDisposition(
      config.contentDispositionType,
      fileName(href, transformed.contentType),
    ),
    "content-security-policy": config.contentSecurityPolicy,
    "x-content-type-options": "nosniff",
  };
  if (transformed.passthrough) headers[IMAGE_PASSTHROUGH] = "1";

  return { status: 200, headers, body: transformed.bytes };
}

function contentEtag(bytes: Uint8Array): string {
  return createHash("sha256").update(bytes).digest("base64url");
}

function upstreamEtag(source: UpstreamImage): string {
  return source.etag
    ? Buffer.from(source.etag).toString("base64url")
    : contentEtag(source.bytes);
}

function fileName(href: string, contentType: string): string {
  const pathname = href.split("?")[0] ?? "";
  const base = pathname.split("/").pop() ?? "";
  const stem = (base.split(".")[0] ?? "").replace(/[^A-Za-z0-9_-]/g, "") || "image";
  const extension = extensionFor(contentType) ?? "bin";
  return `${stem}.${extension}`;
}

function contentDisposition(type: string, filename: string): string {
  const disposition = type === "attachment" ? "attachment" : "inline";
  return `${disposition}; filename="${filename}"`;
}

function encode(value: string): Uint8Array {
  return new TextEncoder().encode(value);
}
