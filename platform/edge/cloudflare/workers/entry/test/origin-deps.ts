import type { OriginReach, OriginStore } from "@platform/edge-contract/origin-reach";
import type { PointerRecordResult, ReleasesBinding, StoredRecord } from "../src/releases";

export const FN_URL = "https://abc123.lambda-url.eu-west-2.on.aws/";

export function makeRecord(over: Partial<StoredRecord> = {}): StoredRecord {
  return {
    app: "api",
    framework: "node",
    release: "deploy-1",
    buildId: "deploy-1",
    rootFunction: "api",
    functionUrls: { api: FN_URL },
    assetPrefix: "",
    pagesPrefix: "",
    createdAt: 1_000,
    edge: {},
    ...over,
  };
}

export function answerEveryRecordWith(
  answer: (args: { slug: string; app?: string }) => Promise<PointerRecordResult>,
): ReleasesBinding {
  return {
    readPointerRecord: answer,
    readLabelRecord: answer,
    readServedRecord: async () => ({ kind: "not-served" }),
    readGate: async () => undefined,
  };
}

export function capturing(): { calls: Request[]; fetch: typeof fetch } {
  const calls: Request[] = [];
  return {
    calls,
    fetch: (async (input: RequestInfo | URL, init?: RequestInit) => {
      calls.push(new Request(input as RequestInfo, init));
      return new Response("origin");
    }) as typeof fetch,
  };
}

export async function withGlobalFetch<T>(
  replacement: typeof fetch,
  run: () => Promise<T>,
): Promise<T> {
  const original = globalThis.fetch;
  globalThis.fetch = replacement;
  try {
    return await run();
  } finally {
    globalThis.fetch = original;
  }
}

export interface AwsOrigin {
  bucket?: string;
  region?: string;
  optimizerUrl?: string;
}

export function awsOrigin(over: AwsOrigin = {}): string {
  const region = over.region ?? "eu-west-2";
  const keys = { accessKeyId: "AKIAEXAMPLE", secretAccessKey: "secretkey" };
  const origin: OriginReach = {
    reach: { kind: "sigv4", service: "lambda", region, ...keys },
    throttle: { status: 429, header: "x-amzn-errortype" },
  };
  if (over.bucket) {
    origin.store = {
      url: `https://${over.bucket}.s3.${region}.amazonaws.com`,
      region,
      holdsEntries: true,
      ...keys,
    };
  }
  if (over.optimizerUrl) {
    origin.images = { optimizerUrl: over.optimizerUrl, maxRequestBytes: 6225920 };
  }
  return JSON.stringify(origin);
}

export function certificateBinding(answer: () => Response = () => new Response("ok")) {
  const sent: Request[] = [];
  const binding = {
    fetch: async (request: Request) => {
      sent.push(request);
      return answer();
    },
  } as unknown as Fetcher;
  return { binding, sent };
}

export interface CertificateOriginEnv {
  OCEL_ORIGIN: string;
  OCEL_ORIGIN_CLIENT_CERTIFICATE: Fetcher;
}

export function storeOrigin(store: Partial<OriginStore> = {}): CertificateOriginEnv {
  const origin: OriginReach = {
    reach: { kind: "client-certificate", certificate: "certificate-id" },
    store: {
      url: "https://objects.example.com/ocel-acme-production",
      prefix: "assets/",
      region: "auto",
      holdsEntries: false,
      accessKeyId: "GOOG1EXAMPLEACCESSID",
      secretAccessKey: "examplesecret",
      ...store,
    },
    throttle: { status: 429, bodyContains: "no available instance" },
    images: { maxRequestBytes: 33554432 },
  };
  return {
    OCEL_ORIGIN: JSON.stringify(origin),
    OCEL_ORIGIN_CLIENT_CERTIFICATE: certificateBinding().binding,
  };
}
