import { S3Client } from "@aws-sdk/client-s3";
import type { ImageOriginRequest, OriginResponse } from "./contract.mjs";
import { SUBSTRATE_MESSAGE } from "./errors.mjs";
import { optimize, type OptimizeDeps } from "./optimize.mjs";
import { s3Store } from "./store.mjs";

// The Lambda entrypoint: the account-global image optimizer behind a Function
// URL, and the only file here that knows about Lambda at all.
//
// ---------------------------------------------------------------------------
// HTTP contract (this is what PR 5b wires the worker to)
// ---------------------------------------------------------------------------
//
//   POST /                       Function URL, AWS_IAM auth, RESPONSE_STREAM
//   Content-Type: application/json
//   Body: the ImageOriginRequest JSON object, verbatim as
//         workers/nextjs/src/image.ts builds it:
//         {slug, app, buildId, url, w, q, accept, mimeType, configHash}
//
// The path is ignored; only the method is checked. No request header is read
// for any purpose other than nothing — see below.
//
//   200  the image. Headers: content-type, cache-control (the UPSTREAM image
//        server's directive verbatim, absent when it sent none), etag,
//        content-disposition, content-security-policy,
//        x-content-type-options: nosniff (divergence 8 — Next emits none), and
//        x-ocel-image-passthrough: 1 when the bytes are an untransformed
//        original because the transform failed.
//   400  a bare text body carrying Next's own message. No content-type, no
//        cache-control — the edge relays it as-is and its own 400s look the same.
//   500  a bare "Internal Server Error", for the urls Next itself cannot parse.
//   502  a generic bare message. The substrate could not answer: no config at
//        the key, a config that does not hash to configHash, or an unexpected
//        throw. The edge does not cache this and serves a stale colo entry if
//        it has one.
//
// Environment:
//   OCEL_IMAGE_ASSET_BUCKET   the account's S3 asset bucket (required)
//   AWS_REGION                supplied by Lambda
//   UV_THREADPOOL_SIZE        pin it in the function config as well; the value
//                             set from JS lands before libuv's pool is created
//                             here, but only because nothing runs first.
//
// ---------------------------------------------------------------------------
// Client headers
// ---------------------------------------------------------------------------
//
// This function never reads a request header and never forwards one. Not
// Cookie, not Authorization, not Accept — the negotiated type arrives in the
// body because the edge's cache key commits to it, and the raw Accept comes
// along only as a field. Every fetch this function makes is unconditionally
// unauthenticated.
//
// CVE-2025-57752 is what that sentence is for: an optimizer that forwarded
// Cookie to the upstream, behind a cache key with no header component, served
// one user's private image to everyone who asked for the same url.

interface FunctionUrlEvent {
  requestContext?: { http?: { method?: string } };
  body?: string;
  isBase64Encoded?: boolean;
}

interface ResponseStream {
  write(chunk: Uint8Array | string): void;
  end(): void;
}

interface AwsLambda {
  streamifyResponse(
    handler: (event: FunctionUrlEvent, stream: ResponseStream) => Promise<void>,
  ): unknown;
  HttpResponseStream: {
    from(
      stream: ResponseStream,
      metadata: { statusCode: number; headers: Record<string, string> },
    ): ResponseStream;
  };
}

// A runtime global, not an import. Absent outside Lambda, which is why it is
// read off globalThis rather than declared: importing this module in a test must
// not depend on being inside the runtime that defines it.
function runtime(): AwsLambda | undefined {
  const global = (globalThis as { awslambda?: AwsLambda }).awslambda;
  return typeof global?.streamifyResponse === "function" ? global : undefined;
}

// One client and one store per container, so a warm invocation reuses the
// connection pool and the credential cache.
let store: ReturnType<typeof s3Store> | undefined;

function assetStore(): ReturnType<typeof s3Store> {
  if (store) return store;
  const bucket = process.env["OCEL_IMAGE_ASSET_BUCKET"];
  if (!bucket) throw new Error("OCEL_IMAGE_ASSET_BUCKET is not set");
  return (store = s3Store(new S3Client({}), bucket));
}

export async function handle(
  event: FunctionUrlEvent,
  deps?: OptimizeDeps,
): Promise<OriginResponse> {
  const method = event.requestContext?.http?.method ?? "POST";
  if (method !== "POST") {
    return { status: 405, headers: {}, body: encode("Method Not Allowed") };
  }
  let payload: ImageOriginRequest;
  try {
    const raw = event.isBase64Encoded
      ? Buffer.from(event.body ?? "", "base64").toString("utf8")
      : (event.body ?? "");
    payload = JSON.parse(raw) as ImageOriginRequest;
    if (typeof payload !== "object" || payload === null || Array.isArray(payload)) {
      throw new Error("payload is not an object");
    }
  } catch (error) {
    console.error("ocel image optimizer: unreadable payload", error);
    // A malformed envelope is not a malformed image request — nothing this
    // function serves reaches it, and only the edge ever writes one.
    return { status: 400, headers: {}, body: encode("Bad Request") };
  }
  let resolved: OptimizeDeps;
  try {
    resolved = deps ?? { store: assetStore() };
  } catch (error) {
    // A misconfigured function is the substrate failing, not the request being
    // wrong, and 502 is the status the edge will not cache.
    console.error("ocel image optimizer: not configured", error);
    return { status: 502, headers: {}, body: encode(SUBSTRATE_MESSAGE) };
  }
  return optimize(payload, resolved);
}

function encode(value: string): Uint8Array {
  return new TextEncoder().encode(value);
}

// Response streaming, matching the Function URL pattern every other origin in
// the substrate uses: the prelude carries status and headers, the body follows.
// The bytes are already whole by the time they are written — a transform has no
// partial output — so this buys the 20 MB streamed payload ceiling rather than
// the 6 MB buffered one, not incrementality.
export const handler = buildHandler();

function buildHandler(): unknown {
  const lambda = runtime();
  if (!lambda) return undefined;
  return lambda.streamifyResponse(async (event, stream) => {
    const response = await handle(event);
    const out = lambda.HttpResponseStream.from(stream, {
      statusCode: response.status,
      headers: response.headers,
    });
    out.write(response.body);
    out.end();
  });
}
