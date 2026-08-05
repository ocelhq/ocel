// The queue's wire format.
//
// The message names no host. It names a route — `isrPrefix` (which deploy) and
// `routeId` (which of that deploy's functions serves it) — and the consumer
// looks the origin up in the record the deploy itself wrote (see origin.mts).
//
// Naming no host is not the same as naming nothing, though: `isrPrefix` chooses
// the S3 key that record is read from, so it is the one field a lying edge can
// still steer with. It is validated here as strictly as `routePath` is, for the
// same reason — see `isKeyPrefix`.
//
// The message is prepared by the edge adapter, which knows the route and the
// framework; the consumer understands neither (§8). What the edge does declare
// is the receipt it expects back, which is the whole of the framework contract
// the consumer evaluates.
export interface RevalidationMessage {
  v: 1;
  headers: Record<string, string>;
  expect: { header: string; value: string } | null;
  isrPrefix: string;
  routeId: string;
  routePath: string;
  lastModified: number;
  enqueuedAt: number;
}

export type Rejection = "malformed" | "unsupported-version";

export type ParseResult = { ok: true; message: RevalidationMessage } | { ok: false; reason: Rejection };

// RFC 9110's field-name token. A name outside it throws inside `new Headers`
// when the trigger is signed, which would classify a permanently broken record
// as a transient handler error and spend five redeliveries on it. It is a
// malformed message, and it is cheaper to say so here.
const headerName = /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/;

function headerMap(value: unknown): Record<string, string> | undefined {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return undefined;
  const entries = Object.entries(value);
  if (entries.some(([k, v]) => typeof v !== "string" || !headerName.test(k))) return undefined;
  return Object.fromEntries(entries) as Record<string, string>;
}

// `isrPrefix` is the only variable part of the S3 key the deploy record is read
// from, and it is interpolated ahead of the `/origin.json` the consumer appends
// (origin.mts). A `#` or a `?` truncates that suffix, so the message would
// choose the whole key — and the edge holds PutObject under `*/fetch-cache/*`,
// which is enough to plant a record naming any host it likes. So the prefix is
// checked for what it is: dot-free key segments over the characters
// cloud/aws/deploy composes it from (env / slug / sanitized app / build id),
// which admits no separator, no traversal, no absolute key and nothing empty.
const keySegment = /^[A-Za-z0-9._-]+$/;

function isKeyPrefix(value: string): boolean {
  return value
    .split("/")
    .every((segment) => segment !== "." && segment !== ".." && keySegment.test(segment));
}

function expectation(value: unknown): RevalidationMessage["expect"] | undefined {
  if (value === null) return null;
  if (typeof value !== "object") return undefined;
  const { header, value: expected } = value as { header?: unknown; value?: unknown };
  if (typeof header !== "string" || typeof expected !== "string") return undefined;
  return { header, value: expected };
}

export function parseMessage(body: string): ParseResult {
  let raw: Record<string, unknown>;
  try {
    raw = JSON.parse(body) as Record<string, unknown>;
  } catch {
    return { ok: false, reason: "malformed" };
  }
  if (typeof raw !== "object" || raw === null) return { ok: false, reason: "malformed" };
  if (raw.v !== 1) return { ok: false, reason: "unsupported-version" };

  const headers = headerMap(raw.headers);
  const expect = expectation(raw.expect);
  const { isrPrefix, routeId, routePath, lastModified, enqueuedAt } = raw;
  if (
    headers === undefined ||
    expect === undefined ||
    typeof isrPrefix !== "string" ||
    typeof routeId !== "string" ||
    typeof routePath !== "string" ||
    typeof lastModified !== "number" ||
    typeof enqueuedAt !== "number"
  ) {
    return { ok: false, reason: "malformed" };
  }
  // A route path is a path under the origin, not a URL. Where the trigger
  // actually lands is checked again after the join (origin.mts); this is the
  // shape check, so a message that could never resolve is rejected before
  // anything reads a deploy record for it.
  if (!routePath.startsWith("/")) return { ok: false, reason: "malformed" };
  if (!isKeyPrefix(isrPrefix)) return { ok: false, reason: "malformed" };

  return {
    ok: true,
    message: { v: 1, headers, expect, isrPrefix, routeId, routePath, lastModified, enqueuedAt },
  };
}
