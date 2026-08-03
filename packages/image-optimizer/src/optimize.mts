import { createHash } from "node:crypto";
import {
  IMAGE_PASSTHROUGH,
  type CompiledImageConfig,
  type ImageOriginRequest,
  type OriginResponse,
} from "./contract.mjs";
import { loadImageConfig } from "./config.mjs";
import { ImageError, SUBSTRATE_MESSAGE, upstreamFailure } from "./errors.mjs";
import { assetKey, identity, type BuildIdentity } from "./keys.mjs";
import { extensionFor } from "./sniff.mjs";
import type { ObjectStore } from "./store.mjs";
import { transform, type Transformed } from "./transform.mjs";
import { fetchUpstream, type UpstreamDeps, type UpstreamImage } from "./upstream.mjs";
import { validate } from "./validate.mjs";

// The request pipeline, from the edge's payload to the bytes that answer it.
//
// The worker holds zero authority, so the sequence is: load the config, prove
// it is the build's, re-run the whole validation against it, and only then
// spend a socket or a decode on anything. Nothing the edge said is taken as
// already-checked.

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
      // Bare, with no content type and no cache directive — which is what Next
      // answers a rejected image request with, and what the edge's own 400s
      // already look like.
      return { status: error.status, headers: {}, body: encode(error.message) };
    }
    // Anything else is the substrate failing, not the request being wrong. 502
    // is the status the edge refuses to cache and answers a stale colo entry
    // for, so a transient inconsistency cannot be frozen in for minimumCacheTTL.
    console.error("ocel image optimizer: substrate failure", error);
    return { status: 502, headers: {}, body: encode(SUBSTRATE_MESSAGE) };
  }
}

async function run(
  payload: ImageOriginRequest,
  deps: OptimizeDeps,
): Promise<OriginResponse> {
  const id = identity(payload);
  const config = await loadImageConfig(deps.store, id, payload.configHash);

  const result = validate(payload, config);
  if (!result.ok) throw new ImageError(result.status, result.message);
  const { href, isAbsolute } = result.params;

  const source = isAbsolute
    ? await fetchUpstream(href, config, deps.upstream)
    : await readLocal(deps.store, id, href, config);

  const transformed = await transform({
    bytes: source.bytes,
    // Relayed as it arrived rather than coerced: transform() checks it against
    // config.formats, so a value that is not a string it could have negotiated
    // is a refusal there instead of being silently read as "no negotiation".
    mimeType: payload.mimeType,
    width: result.params.width,
    quality: result.params.quality,
    config,
  });

  return respond(href, source, transformed, config);
}

// A local image is read out of the same bucket and under the same key the deploy
// wrote it to, never off a filesystem and never through the app's own server.
// It is the app's own build output, but it is still bytes an attacker chose the
// path of, so it gets the same ceiling the remote path does.
async function readLocal(
  store: ObjectStore,
  id: BuildIdentity,
  href: string,
  config: CompiledImageConfig,
): Promise<UpstreamImage> {
  const url = new URL(href, "http://n");
  const key = assetKey(id, url.pathname);
  let object;
  try {
    object = await store.get(key, config.maximumResponseBody);
  } catch (error) {
    throw upstreamFailure(error);
  }
  // A path the build never emitted is the request being wrong about what this
  // app serves, not the substrate failing to serve it.
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
    // The upstream's directive, verbatim and unmodified. The edge never talks to
    // that server, so relaying it is the only way its freshness reaches the tier
    // that caches on it; the edge parses it for a TTL and then replaces it, so
    // it never reaches a browser. Absent upstream means absent here, which the
    // edge reads as minimumCacheTTL.
    ...(source.cacheControl ? { "cache-control": source.cacheControl } : {}),
    etag: transformed.unmodified
      ? upstreamEtag(source)
      : contentEtag(transformed.bytes),
    "content-disposition": contentDisposition(
      config.contentDispositionType,
      fileName(href, transformed.contentType),
    ),
    "content-security-policy": config.contentSecurityPolicy,
    // Divergence 8. Next emits none. This route serves attacker-influenced bytes
    // from the app's own origin under a content type this function picked, which
    // is where content-type confusion pays off — a bypass type is returned
    // unmodified under a type the sniffer inferred, not one the source declared.
    // Free, and it bounds the damage of any future sniffer defect; review found
    // exactly one (a wildcard sentinel that made image/x-icon match almost
    // anything). Served images only: the 400/500/502 answers carry no header
    // block at all, which is what the edge and the conformance fixtures expect.
    "x-content-type-options": "nosniff",
  };
  // Read by the edge to force ttl = minimumCacheTTL and then stripped, so a
  // failed transform is never held for as long as the upstream asked for the
  // image it failed on.
  if (transformed.passthrough) headers[IMAGE_PASSTHROUGH] = "1";

  return { status: 200, headers, body: transformed.bytes };
}

// base64url, matching Next: an etag travels in a header where a raw upstream
// value may be weak or quoted, and Next stores it in a filename.
function contentEtag(bytes: Uint8Array): string {
  return createHash("sha256").update(bytes).digest("base64url");
}

function upstreamEtag(source: UpstreamImage): string {
  return source.etag
    ? Buffer.from(source.etag).toString("base64url")
    : contentEtag(source.bytes);
}

// The filename a download would land under: the source's own basename with the
// extension of what we actually produced. Reduced to a conservative character
// set rather than quoted — a header value is not the place to be clever about
// escaping, and no useful filename needs anything else.
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
