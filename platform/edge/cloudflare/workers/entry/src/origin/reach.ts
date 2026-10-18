import type { OriginThrottle } from "@framework/next-router/retry";
import type {
  OriginImages,
  OriginReach,
  OriginStore,
  Throttle,
} from "@platform/edge-contract/origin-reach";
import { AwsClient } from "aws4fetch";
import type { Env } from "../env";
import type { StoreSource } from "../store-read";
import { clientCertificateOriginFetch } from "./client-certificate";
import { sigv4OriginFetch } from "./sigv4";
import { type OpenedStore, type OriginObjects, openStore } from "./store";

export type OriginEnv = Pick<Env, "OCEL_ORIGIN" | "OCEL_ORIGIN_CLIENT_CERTIFICATE">;

export interface Origin {
  fetch: typeof fetch;
  isThrottle: OriginThrottle;
  objects?: OriginObjects;
  entries?: StoreSource;
  images?: OriginImages;
}

interface ReadOrigin {
  signedFetch?: typeof fetch;
  isThrottle: OriginThrottle;
  store?: OpenedStore;
  images?: OriginImages;
}

type Fields = Record<string, unknown>;

function refuse(problem: string): never {
  throw new Error(`ocel: OCEL_ORIGIN ${problem}; this worker cannot reach its origin`);
}

function isFields(value: unknown): value is Fields {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function readFields(value: unknown, what: string): Fields {
  if (!isFields(value)) refuse(`names no ${what}`);
  return value;
}

function readText(fields: Fields, name: string, what: string): string {
  const value = fields[name];
  if (typeof value !== "string" || value === "") refuse(`names ${what} with no ${name}`);
  return value;
}

function readOptionalText(fields: Fields, name: string, what: string): string | undefined {
  const value = fields[name];
  if (value !== undefined && typeof value !== "string") {
    refuse(`names ${what} whose ${name} is not text`);
  }
  return value;
}

function parseDescriptor(raw: string | undefined): Fields {
  if (raw === undefined) refuse("is not bound to this worker");
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    refuse("is not JSON");
  }
  return readFields(parsed, "origin");
}

function readSignedFetch(reach: Fields): typeof fetch | undefined {
  const kind = reach.kind;
  if (kind === "client-certificate") return undefined;
  if (kind !== "sigv4") {
    refuse(`names the reach ${JSON.stringify(kind)}, which this worker does not know`);
  }
  const what = "a signed reach";
  return sigv4OriginFetch(
    new AwsClient({
      service: readText(reach, "service", what),
      region: readText(reach, "region", what),
      accessKeyId: readText(reach, "accessKeyId", what),
      secretAccessKey: readText(reach, "secretAccessKey", what),
    }),
  );
}

function readThrottle(value: unknown): OriginThrottle {
  const throttle = readFields(value, "throttle");
  const { status } = throttle;
  if (typeof status !== "number" || !Number.isInteger(status) || status < 100 || status > 599) {
    refuse("names a throttle with no status");
  }
  return newThrottleCheck({
    status,
    header: readOptionalText(throttle, "header", "a throttle"),
    bodyContains: readOptionalText(throttle, "bodyContains", "a throttle"),
  });
}

function newThrottleCheck({ status, header, bodyContains }: Throttle): OriginThrottle {
  return (response) => {
    if (response.status !== status) return false;
    if (header !== undefined && !response.headers.has(header)) return false;
    if (bodyContains === undefined) return true;
    return response
      .clone()
      .text()
      .then((text) => text.includes(bodyContains));
  };
}

function readStore(value: unknown): OriginStore | undefined {
  if (value === undefined) return undefined;
  const store = readFields(value, "store");
  const what = "a store";
  const url = readText(store, "url", what);
  if (!URL.canParse(url)) refuse("names a store whose url is not a URL");
  if (typeof store.holdsEntries !== "boolean") {
    refuse("names a store that says not whether it holds entries");
  }
  return {
    url,
    prefix: readOptionalText(store, "prefix", what),
    region: readText(store, "region", what),
    holdsEntries: store.holdsEntries,
    entriesPrefix: readOptionalText(store, "entriesPrefix", what),
    accessKeyId: readText(store, "accessKeyId", what),
    secretAccessKey: readText(store, "secretAccessKey", what),
  };
}

function readImages(value: unknown): OriginImages | undefined {
  if (value === undefined) return undefined;
  const images = readFields(value, "images");
  const { maxRequestBytes } = images;
  if (
    maxRequestBytes !== undefined &&
    (typeof maxRequestBytes !== "number" ||
      !Number.isInteger(maxRequestBytes) ||
      maxRequestBytes <= 0)
  ) {
    refuse("names an image request limit that is not a positive number of bytes");
  }
  const optimizerUrl = readOptionalText(images, "optimizerUrl", "images");
  return {
    ...(optimizerUrl !== undefined && { optimizerUrl }),
    ...(maxRequestBytes !== undefined && { maxRequestBytes }),
  };
}

function readOrigin(raw: string | undefined): ReadOrigin {
  const descriptor = parseDescriptor(raw) as Partial<Record<keyof OriginReach, unknown>>;
  const store = readStore(descriptor.store);
  return {
    signedFetch: readSignedFetch(readFields(descriptor.reach, "reach")),
    isThrottle: readThrottle(descriptor.throttle),
    store: store && openStore(store),
    images: readImages(descriptor.images),
  };
}

let held: { raw: string; origin: ReadOrigin } | undefined;

function readHeldOrigin(raw: string | undefined): ReadOrigin {
  if (held !== undefined && held.raw === raw) return held.origin;
  const origin = readOrigin(raw);
  held = { raw: raw as string, origin };
  return origin;
}

function certificateFetch(binding: Fetcher | undefined): typeof fetch {
  if (!binding) {
    refuse(
      "names a client certificate reach, and this worker has no OCEL_ORIGIN_CLIENT_CERTIFICATE binding to present it with",
    );
  }
  return clientCertificateOriginFetch(binding);
}

export function openOrigin(env: OriginEnv): Origin {
  const { signedFetch, isThrottle, store, images } = readHeldOrigin(env.OCEL_ORIGIN);
  return {
    fetch: signedFetch ?? certificateFetch(env.OCEL_ORIGIN_CLIENT_CERTIFICATE),
    isThrottle,
    objects: store?.objects,
    entries: store?.entries,
    images,
  };
}
