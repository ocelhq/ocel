// The queue's wire format, and the only door a record gets through.
//
// The message is prepared by the edge adapter, which knows the route and the
// framework; the consumer understands none of that (§8). What it does
// understand is that the record's headers carry the app's bypass token, so a
// message naming a host that is not this deploy's own would exfiltrate that
// token together with a valid signature. Host validation therefore happens
// here, inside the only function that can produce a RevalidationMessage: a
// caller holding one is holding a checked one, without having to remember to
// check.
export interface RevalidationMessage {
  v: 1;
  url: string;
  headers: Record<string, string>;
  expect: { header: string; value: string } | null;
  isrPrefix: string;
  routePath: string;
  lastModified: number;
  enqueuedAt: number;
}

export type Rejection = "malformed" | "unsupported-version" | "host-not-permitted";

export type ParseResult = { ok: true; message: RevalidationMessage } | { ok: false; reason: Rejection };

// The hosts this deploy may be told to trigger, from OCEL_REVALIDATE_ALLOWED_HOSTS
// (see README). Exact Function URL hosts, comma-separated; unset or empty
// permits nothing, so a function that was never told its own origin triggers
// nothing rather than anything.
export function permittedHosts(value: string | undefined): ReadonlySet<string> {
  return new Set(
    (value ?? "")
      .split(",")
      .map((host) => host.trim().toLowerCase())
      .filter((host) => host !== ""),
  );
}

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

function permits(hosts: ReadonlySet<string>, url: string): boolean {
  let parsed: URL;
  try {
    parsed = new URL(url);
  } catch {
    return false;
  }
  // `host` rather than `hostname`, so a port cannot smuggle the request
  // somewhere the pinned host does not answer.
  return parsed.protocol === "https:" && hosts.has(parsed.host.toLowerCase());
}

export function parseMessage(body: string, hosts: ReadonlySet<string>): ParseResult {
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
  const { url, isrPrefix, routePath, lastModified, enqueuedAt } = raw;
  if (
    headers === undefined ||
    expect === undefined ||
    typeof url !== "string" ||
    typeof isrPrefix !== "string" ||
    typeof routePath !== "string" ||
    typeof lastModified !== "number" ||
    typeof enqueuedAt !== "number"
  ) {
    return { ok: false, reason: "malformed" };
  }

  if (!permits(hosts, url)) return { ok: false, reason: "host-not-permitted" };

  return {
    ok: true,
    message: { v: 1, url, headers, expect, isrPrefix, routePath, lastModified, enqueuedAt },
  };
}
