import { createPrivateKey, randomBytes, sign } from "node:crypto";
import type { RealtimeProperties } from "../gen/proto/common/bindings/v1/bindings_pb.js";

/** The operation a token admits its holder to on its channel. */
export type TokenOperation = "connect" | "subscribe" | "publish";

/** The realtime resource a token is minted for: its name and how long its tokens live. */
export interface TokenIssuer {
  name: string;
  ttlSeconds: number;
}

/** A signed token and the Unix second it expires at. */
export interface Token {
  token: string;
  expiresAt: number;
}

const tokenHeader = { alg: "EdDSA", typ: "JWT" };
const pkcs8Ed25519Prefix = Buffer.from("302e020100300506032b657004220420", "hex");

function encodeCanonicalJson(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(encodeCanonicalJson).join(",")}]`;
  if (value !== null && typeof value === "object") {
    const members = Object.keys(value)
      .sort()
      .map(
        (key) =>
          `${JSON.stringify(key)}:${encodeCanonicalJson((value as Record<string, unknown>)[key])}`,
      );
    return `{${members.join(",")}}`;
  }
  return JSON.stringify(value);
}

function encodeBase64url(text: string | Buffer): string {
  return Buffer.from(text).toString("base64url");
}

/** Signs `header` and `claims` as an EdDSA JWT with an Ed25519 signing key. */
export function signToken(
  signingKey: Uint8Array,
  header: Record<string, unknown>,
  claims: Record<string, unknown>,
): string {
  const key = createPrivateKey({
    key: Buffer.concat([pkcs8Ed25519Prefix, Buffer.from(signingKey)]),
    format: "der",
    type: "pkcs8",
  });
  const input = `${encodeBase64url(encodeCanonicalJson(header))}.${encodeBase64url(encodeCanonicalJson(claims))}`;
  return `${input}.${encodeBase64url(sign(null, Buffer.from(input), key))}`;
}

/**
 * Mints the token admitting `subject` to `operation` on `channel` of `issuer`, signed with
 * the binding's key for the binding's host. Throws when the binding's key cannot sign.
 */
export function mintToken(
  properties: RealtimeProperties,
  issuer: TokenIssuer,
  subject: string,
  operation: TokenOperation,
  channel: string,
): Token {
  const issuedAt = Math.floor(Date.now() / 1_000);
  const expiresAt = issuedAt + issuer.ttlSeconds;
  const token = signToken(properties.signingKey, tokenHeader, {
    iss: `ocel:rt:${issuer.name}`,
    aud: properties.host,
    sub: subject,
    iat: issuedAt,
    exp: expiresAt,
    jti: randomBytes(16).toString("base64url"),
    ocel: { op: operation, ch: channel, ns: issuer.name },
  });
  return { token, expiresAt };
}
