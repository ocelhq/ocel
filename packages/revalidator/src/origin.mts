import { AwsClient } from "aws4fetch";

import { originTimeoutMs } from "./limits.mjs";
import type { RevalidationMessage } from "./message.mjs";

// Where a trigger may be sent.
//
// An unexported class with a private field, constructed at exactly one place —
// the end of `compose`, below, after the deploy's own record has been read.
// Being unexported is what the class buys over the brand it replaced: outside
// this module a Target cannot be constructed, assigned from a literal, or built
// by copying a real one, because the private field survives none of those. It
// is a seam, not a theorem — `as Target` still compiles, as it does for any
// TypeScript type, and origin.test.mts says so out loud rather than leaving a
// comment here to overstate it.
//
// The property that actually keeps the app's bypass token off a host of the
// edge's choosing lives elsewhere: nothing on the wire names a host, and
// `isrPrefix` — the one field that steers where the record is read from — is
// validated in message.mts as the key prefix a deploy builds. So the only host
// this consumer signs for is the one that deploy recorded.
class ResolvedTarget {
  readonly #resolved = true;

  constructor(
    readonly url: string,
    readonly region: string,
  ) {}
}

export type Target = ResolvedTarget;

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
