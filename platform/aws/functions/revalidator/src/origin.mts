import { AwsClient } from "aws4fetch";

import { originTimeoutMs } from "./limits.mjs";
import type { RevalidationMessage } from "./message.mjs";

class ResolvedTarget {
  readonly #resolved = true;

  constructor(
    readonly url: string,
    readonly region: string,
  ) {}
}

export type Target = ResolvedTarget;

export type ResolveFailure =
  | "origin-unconfigured"
  | "origin-unavailable"
  | "origin-unusable";

export type Resolution = { ok: true; target: Target } | { ok: false; reason: ResolveFailure };

export const originDocumentName = "origin.json";

type RouteUrls = { ok: true; urls: Record<string, string> } | { ok: false; reason: ResolveFailure };

export interface OriginDeps {
  fetch: typeof fetch;
  credentials: { accessKeyId: string; secretAccessKey: string; sessionToken?: string };
  bucket?: string;
  region?: string;
  origins: Map<string, Promise<RouteUrls>>;
  originTimeoutMs?: number;
}

const functionUrlHost = /^[a-z0-9]+\.lambda-url\.([a-z0-9-]+)\.on\.aws$/;

function regionOf(host: string): string | undefined {
  return functionUrlHost.exec(host)?.[1];
}

function compose(origin: string, routePath: string): Resolution {
  let base: URL;
  let url: URL;
  try {
    base = new URL(origin);
    url = new URL(routePath, base);
  } catch {
    return { ok: false, reason: "origin-unusable" };
  }
  if (base.protocol !== "https:" || url.origin !== base.origin) {
    return { ok: false, reason: "origin-unusable" };
  }
  const region = regionOf(base.host);
  if (region === undefined) return { ok: false, reason: "origin-unusable" };
  return { ok: true, target: new ResolvedTarget(url.toString(), region) };
}

function document(body: string): RouteUrls {
  let doc: unknown;
  try {
    doc = JSON.parse(body);
  } catch {
    return { ok: false, reason: "origin-unusable" };
  }
  if (typeof doc !== "object" || doc === null) return { ok: false, reason: "origin-unusable" };
  const { v, functionUrls } = doc as { v?: unknown; functionUrls?: unknown };
  if (v !== 1) return { ok: false, reason: "origin-unusable" };
  if (typeof functionUrls !== "object" || functionUrls === null || Array.isArray(functionUrls)) {
    return { ok: false, reason: "origin-unusable" };
  }
  const urls = Object.entries(functionUrls).filter(([, url]) => typeof url === "string");
  return { ok: true, urls: Object.fromEntries(urls) as Record<string, string> };
}

async function read(deps: OriginDeps, isrPrefix: string): Promise<RouteUrls> {
  const { bucket, region } = deps;
  if (bucket === undefined || bucket === "" || region === undefined || region === "") {
    return { ok: false, reason: "origin-unconfigured" };
  }

  const url = `https://${bucket}.s3.${region}.amazonaws.com/${isrPrefix}/${originDocumentName}`;
  const client = new AwsClient({ ...deps.credentials, service: "s3", region });
  const signed = await client.sign(url, { method: "GET" });

  const signal = AbortSignal.timeout(deps.originTimeoutMs ?? originTimeoutMs);
  try {
    const response = await deps.fetch(url, { method: "GET", headers: signed.headers, signal });
    if (!response.ok) {
      return { ok: false, reason: response.status === 404 ? "origin-unusable" : "origin-unavailable" };
    }
    return document(await response.text());
  } catch {
    return { ok: false, reason: "origin-unavailable" };
  }
}

function routeUrls(deps: OriginDeps, isrPrefix: string): Promise<RouteUrls> {
  const memo = deps.origins.get(isrPrefix);
  if (memo !== undefined) return memo;
  const pending = read(deps, isrPrefix);
  deps.origins.set(isrPrefix, pending);
  void pending.catch(() => {});
  return pending;
}

export async function resolve(deps: OriginDeps, message: RevalidationMessage): Promise<Resolution> {
  const record = await routeUrls(deps, message.isrPrefix);
  if (!record.ok) return record;
  const origin = record.urls[message.routeId];
  if (typeof origin !== "string") return { ok: false, reason: "origin-unusable" };
  return compose(origin, message.routePath);
}
