import { createPublicKey, type JsonWebKey, type KeyObject, verify } from "node:crypto";

export interface IdTokenOptions {
  audience: string;
  email: string;
  fetch?: typeof fetch;
  now?: () => number;
  sleep?: (ms: number) => Promise<void>;
  random?: () => number;
  certsUrl?: string;
}

export type IdTokenCheck = (authorization: string | undefined) => Promise<boolean>;

const defaultCertsUrl = "https://www.googleapis.com/oauth2/v3/certs";
const defaultKeyLifetimeMs = 3_600_000;
const unknownKeyRefetchIntervalMs = 60_000;
const issuedAtSkewMs = 60_000;
const attempts = 3;
const attemptTimeoutMs = 2_500;
const issuers = new Set(["https://accounts.google.com", "accounts.google.com"]);

interface KeySet {
  keys: Map<string, KeyObject>;
  expiresAt: number;
}

interface TokenParts {
  header: string;
  payload: string;
  signature: string;
  kid: string;
  claims: Record<string, unknown>;
}

function decodeObject(part: string): Record<string, unknown> | undefined {
  try {
    const value: unknown = JSON.parse(Buffer.from(part, "base64url").toString("utf8"));
    if (typeof value !== "object" || value === null || Array.isArray(value)) return undefined;
    return value as Record<string, unknown>;
  } catch {
    return undefined;
  }
}

function parseToken(authorization: string | undefined): TokenParts | undefined {
  const match = authorization === undefined ? null : /^bearer\s+(\S+)$/i.exec(authorization);
  if (!match) return undefined;
  const parts = match[1]!.split(".");
  if (parts.length !== 3 || parts.some((part) => part === "")) return undefined;
  const [header, payload, signature] = parts as [string, string, string];
  const head = decodeObject(header);
  if (head?.alg !== "RS256" || typeof head.kid !== "string") return undefined;
  const claims = decodeObject(payload);
  if (!claims) return undefined;
  return { header, payload, signature, kid: head.kid, claims };
}

function keyLifetimeMs(res: Response): number {
  const maxAge = /max-age=(\d+)/i.exec(res.headers.get("cache-control") ?? "");
  if (!maxAge) return defaultKeyLifetimeMs;
  const age = Number(res.headers.get("age") ?? 0);
  return Math.max(0, Number(maxAge[1]) - (Number.isFinite(age) ? age : 0)) * 1000;
}

function readKeys(jwks: unknown): Map<string, KeyObject> | undefined {
  const list = (jwks as { keys?: unknown } | null)?.keys;
  if (!Array.isArray(list)) return undefined;
  const keys = new Map<string, KeyObject>();
  for (const jwk of list as (JsonWebKey & { kid?: unknown; alg?: unknown })[]) {
    if (jwk?.kty !== "RSA" || typeof jwk.kid !== "string") continue;
    if (jwk.alg !== undefined && jwk.alg !== "RS256") continue;
    try {
      keys.set(jwk.kid, createPublicKey({ key: jwk, format: "jwk" }));
    } catch {}
  }
  return keys;
}

function claimsAccepted(claims: Record<string, unknown>, options: IdTokenOptions, now: number) {
  const { iss, aud, exp, iat, email, email_verified: verified } = claims;
  return (
    typeof iss === "string" &&
    issuers.has(iss) &&
    aud === options.audience &&
    typeof exp === "number" &&
    exp * 1000 > now &&
    typeof iat === "number" &&
    iat * 1000 <= now + issuedAtSkewMs &&
    email === options.email &&
    verified === true
  );
}

export function newGoogleIdTokenCheck(options: IdTokenOptions): IdTokenCheck {
  const doFetch = options.fetch ?? globalThis.fetch;
  const now = options.now ?? Date.now;
  const sleep = options.sleep ?? ((ms: number) => new Promise<void>((r) => setTimeout(r, ms)));
  const random = options.random ?? Math.random;
  const certsUrl = options.certsUrl ?? defaultCertsUrl;

  let cached: KeySet | undefined;
  let inflight: Promise<KeySet> | undefined;
  let lastUnknownKidRefetch: number | undefined;

  async function attempt(): Promise<{ retry: boolean; failure: string } | KeySet> {
    let res: Response;
    try {
      res = await doFetch(certsUrl, { signal: AbortSignal.timeout(attemptTimeoutMs) });
    } catch (err) {
      return { retry: true, failure: err instanceof Error ? err.message : "network error" };
    }
    if (!res.ok) {
      const retry = res.status === 429 || res.status >= 500;
      return { retry, failure: String(res.status) };
    }
    let keys: Map<string, KeyObject> | undefined;
    try {
      keys = readKeys(await res.json());
    } catch {
      keys = undefined;
    }
    if (!keys) return { retry: false, failure: "unreadable response" };
    return { keys, expiresAt: now() + keyLifetimeMs(res) };
  }

  async function fetchKeys(): Promise<KeySet> {
    let failure = "";
    for (let n = 1; n <= attempts; n++) {
      const result = await attempt();
      if ("keys" in result) {
        cached = result;
        return result;
      }
      failure = result.failure;
      if (!result.retry || n === attempts) break;
      await sleep(random() * Math.min(1_000, 100 * 2 ** (n - 1)));
    }
    throw new Error(`ocel: could not read Google's token signing keys: ${failure}`);
  }

  function loadKeys(): Promise<KeySet> {
    inflight ??= fetchKeys().finally(() => {
      inflight = undefined;
    });
    return inflight;
  }

  async function keyFor(kid: string): Promise<KeyObject | undefined> {
    const set = cached && now() < cached.expiresAt ? cached : await loadKeys();
    const known = set.keys.get(kid);
    if (known) return known;
    if (
      lastUnknownKidRefetch !== undefined &&
      now() - lastUnknownKidRefetch < unknownKeyRefetchIntervalMs
    ) {
      return undefined;
    }
    lastUnknownKidRefetch = now();
    return (await loadKeys()).keys.get(kid);
  }

  return async (authorization) => {
    const token = parseToken(authorization);
    if (!token) return false;
    const key = await keyFor(token.kid);
    if (!key) return false;
    try {
      const signed = Buffer.from(`${token.header}.${token.payload}`);
      if (!verify("RSA-SHA256", signed, key, Buffer.from(token.signature, "base64url"))) {
        return false;
      }
    } catch {
      return false;
    }
    return claimsAccepted(token.claims, options, now());
  };
}
