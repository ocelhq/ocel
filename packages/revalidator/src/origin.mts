import { AwsClient } from "aws4fetch";

import { originTimeoutMs } from "./limits.mjs";
import type { RevalidationMessage } from "./message.mjs";

declare const resolved: unique symbol;

// Where a trigger may be sent.
//
// Only `resolve` produces one, and `trigger` accepts nothing else, so "the
// request goes to this deploy's own origin" is a property of the type rather
// than of a check somebody has to remember. The message carries the app's
// bypass token, and the token is safe here for a structural reason: nothing on
// the wire names a host, so the only host the consumer will ever sign for is
// the one the deploy recorded for that isrPrefix.
export interface Target {
  readonly url: string;
  readonly region: string;
  readonly [resolved]: true;
}

export type ResolveFailure =
  // No asset bucket in the environment: the consumer was never told where the
  // deploy records live, so it triggers nothing rather than anything.
  | "origin-unconfigured"
  // The record could not be read — S3 error, timeout, thrown fetch. Transient;
  // redelivery is worth something.
  | "origin-unavailable"
  // The record was read and does not answer for this route. Permanent.
  | "origin-unusable";

export type Resolution = { ok: true; target: Target } | { ok: false; reason: ResolveFailure };

// The deploy record, written by cloud/aws/deploy under the app's own isrPrefix:
// every Function URL this build realized, keyed by the route id the edge
// dispatches to. It is the authority on where a route's origin is, because it
// is written by the only thing that knows — the deploy that created the URL.
export const originDocumentName = "origin.json";

type RouteUrls = { ok: true; urls: Record<string, string> } | { ok: false; reason: ResolveFailure };

export interface OriginDeps {
  fetch: typeof fetch;
  credentials: { accessKeyId: string; secretAccessKey: string; sessionToken?: string };
  // The consumer's own configuration — the substrate's asset bucket and the
  // region it lives in. Never anything the message said.
  bucket?: string;
  region?: string;
  // One entry per isrPrefix, for the life of the invocation. A batch of ten is
  // usually one prefix, and the memo cannot go stale within a redeploy because
  // the isrPrefix carries the build id: a new build is a new key.
  origins: Map<string, Promise<RouteUrls>>;
  originTimeoutMs?: number;
}

// The region a Function URL host names: `<id>.lambda-url.<region>.on.aws`.
// aws4fetch guesses region and service from `*.amazonaws.com` hosts only, and a
// wrong guess is an opaque 403, so it is read here and passed explicitly.
//
// Anchored on the whole host, not scanned for a label: what the deploy recorded
// is what makes the token safe, and the shape is not relied on for that (epic
// decision D) — but reading the region out of an unanchored match also means
// signing for `attacker.lambda-url.us-east-1.evil.example`, which is a host
// this consumer has no business signing anything for.
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
  // The recorded origin decides the host; the route path may only choose a path
  // beneath it. Comparing origins after the join is what makes that true of
  // every string the edge could send, rather than of the ones a pattern
  // happened to anticipate.
  if (base.protocol !== "https:" || url.origin !== base.origin) {
    return { ok: false, reason: "origin-unusable" };
  }
  const region = regionOf(base.host);
  if (region === undefined) return { ok: false, reason: "origin-unusable" };
  return { ok: true, target: { url: url.toString(), region } as Target };
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
  // Signing sits outside the catch deliberately: a signer that throws is a
  // malformed role, not a transient S3 condition, and it belongs in the
  // handler's own catch rather than dressed up as an unreadable record.
  const signed = await client.sign(url, { method: "GET" });

  const signal = AbortSignal.timeout(deps.originTimeoutMs ?? originTimeoutMs);
  try {
    const response = await deps.fetch(url, { method: "GET", headers: signed.headers, signal });
    // A deploy that predates the record answers 404 forever; anything else is
    // worth redelivering.
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
  // A rejection here reaches whichever record awaits it and becomes that
  // record's handler-error. Marking it handled keeps a record whose group was
  // already stopped — and which therefore never awaits it — from turning a
  // reported failure into an unhandled rejection.
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
