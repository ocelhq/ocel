// The queue's wire format.
//
// The message names no host. It names a route — `isrPrefix` (which deploy) and
// `routeId` (which of that deploy's functions serves it) — and the consumer
// looks the origin up in the record the deploy itself wrote (see origin.mts).
// So the edge cannot choose where the app's bypass token is sent, whatever it
// puts in the message: there is no field that could say.
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

function headerMap(value: unknown): Record<string, string> | undefined {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return undefined;
  const entries = Object.entries(value);
  if (entries.some(([, v]) => typeof v !== "string")) return undefined;
  return Object.fromEntries(entries) as Record<string, string>;
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

  return {
    ok: true,
    message: { v: 1, headers, expect, isrPrefix, routeId, routePath, lastModified, enqueuedAt },
  };
}
