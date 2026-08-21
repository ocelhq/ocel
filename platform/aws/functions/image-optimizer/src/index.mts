import { S3Client } from "@aws-sdk/client-s3";
import type { ImageOriginRequest, OriginResponse } from "./contract.mjs";
import { BOOTSTRAP_MESSAGE } from "./errors.mjs";
import { optimize, type OptimizeDeps } from "./optimize.mjs";
import { s3Store } from "./store.mjs";

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

function runtime(): AwsLambda | undefined {
  const global = (globalThis as { awslambda?: AwsLambda }).awslambda;
  return typeof global?.streamifyResponse === "function" ? global : undefined;
}

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
    return { status: 400, headers: {}, body: encode("Bad Request") };
  }
  let resolved: OptimizeDeps;
  try {
    resolved = deps ?? { store: assetStore() };
  } catch (error) {
    console.error("ocel image optimizer: not configured", error);
    return { status: 502, headers: {}, body: encode(BOOTSTRAP_MESSAGE) };
  }
  return optimize(payload, resolved);
}

function encode(value: string): Uint8Array {
  return new TextEncoder().encode(value);
}

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
