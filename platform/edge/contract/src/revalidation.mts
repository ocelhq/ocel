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

const headerName = /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/;

function headerMap(value: unknown): Record<string, string> | undefined {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return undefined;
  const entries = Object.entries(value);
  if (entries.some(([k, v]) => typeof v !== "string" || !headerName.test(k))) return undefined;
  return Object.fromEntries(entries) as Record<string, string>;
}

const keySegment = /^[A-Za-z0-9._-]+$/;

function isKeyPrefix(value: string): boolean {
  return value
    .split("/")
    .every(
      (segment) =>
        segment !== "." && segment !== ".." && segment !== "fetch-cache" && keySegment.test(segment),
    );
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
  if (!routePath.startsWith("/")) return { ok: false, reason: "malformed" };
  if (!isKeyPrefix(isrPrefix)) return { ok: false, reason: "malformed" };

  return {
    ok: true,
    message: { v: 1, headers, expect, isrPrefix, routeId, routePath, lastModified, enqueuedAt },
  };
}
