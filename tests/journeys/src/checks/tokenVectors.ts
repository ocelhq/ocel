import { createPublicKey, verify } from "node:crypto";
import { readFileSync } from "node:fs";
import path from "node:path";
import { repoRoot } from "../paths";

const VECTORS_FILE = path.join(repoRoot, "proto", "realtime", "vectors.json");
const SPKI_ED25519_PREFIX = Buffer.from("302a300506032b6570032100", "hex");
const TIME_CLAIMS = new Set(["iat", "exp"]);

export type TokenClaims = Record<string, unknown> & {
  iat: number;
  exp: number;
  ocel: { op: string; ch: string; ns: string };
};

export type DecodedToken = { header: Record<string, unknown>; claims: TokenClaims };

type TokenCase = { name: string; token: string; valid: boolean; reason?: string };

export type TokenVectors = {
  tokens: {
    cases: TokenCase[];
    expect: { aud: string; ch: string; ns: string };
    mint: { claims: Record<string, unknown> }[];
    now: number;
    verifyKey: string;
  };
};

export type Signer = "binding" | "another-key" | "nobody";

export type Forgery = DecodedToken & { name: string; reason: string; signedBy: Signer };

export function readTokenVectors(): TokenVectors {
  return JSON.parse(readFileSync(VECTORS_FILE, "utf8")) as TokenVectors;
}

function decodeSegment(segment: string | undefined): Record<string, unknown> {
  return JSON.parse(Buffer.from(segment ?? "", "base64url").toString("utf8"));
}

export function decodeToken(token: string): DecodedToken {
  const [header, claims] = token.split(".");
  return { header: decodeSegment(header), claims: decodeSegment(claims) as TokenClaims };
}

function isSignedBy(token: string, verifyKey: string): boolean {
  const [header, claims, signature = ""] = token.split(".");
  const key = createPublicKey({
    key: Buffer.concat([SPKI_ED25519_PREFIX, Buffer.from(verifyKey, "base64")]),
    format: "der",
    type: "spki",
  });
  return verify(null, Buffer.from(`${header}.${claims}`), key, Buffer.from(signature, "base64url"));
}

function applyEditAtEnd(live: string, valid: string, changed: string): string {
  let kept = 0;
  while (kept < valid.length && valid[kept] === changed[kept]) kept += 1;
  return live.slice(0, live.length - (valid.length - kept)) + changed.slice(kept);
}

function forgeClaims(live: TokenClaims, valid: TokenClaims, changed: TokenClaims, now: number) {
  const claims: TokenClaims = { ...live, ocel: { ...live.ocel } };
  for (const [name, value] of Object.entries(changed)) {
    if (name === "ocel" || JSON.stringify(value) === JSON.stringify(valid[name])) continue;
    claims[name] = TIME_CLAIMS.has(name) ? live.iat + (value as number) - now : value;
  }
  const { ocel } = changed;
  if (ocel.op !== valid.ocel.op) claims.ocel.op = ocel.op;
  if (ocel.ns !== valid.ocel.ns) claims.ocel.ns = ocel.ns;
  if (ocel.ch !== valid.ocel.ch)
    claims.ocel.ch = applyEditAtEnd(live.ocel.ch, valid.ocel.ch, ocel.ch);
  return claims;
}

function readSigner(
  changed: TokenCase,
  header: Record<string, unknown>,
  verifyKey: string,
): Signer {
  if (header.alg !== "EdDSA") return "nobody";
  return isSignedBy(changed.token, verifyKey) ? "binding" : "another-key";
}

export function forgeRefusedCases(vectors: TokenVectors, live: DecodedToken): Forgery[] {
  const { cases, now, verifyKey } = vectors.tokens;
  const valid = cases.find((one) => one.valid);
  if (!valid) throw new Error("the shared vectors hold no valid token to measure the others by");
  const measured = decodeToken(valid.token);
  return cases
    .filter((one) => !one.valid)
    .map((one) => {
      const changed = decodeToken(one.token);
      const header =
        JSON.stringify(changed.header) === JSON.stringify(measured.header)
          ? live.header
          : changed.header;
      return {
        name: one.name,
        reason: one.reason ?? "",
        header,
        claims: forgeClaims(live.claims, measured.claims, changed.claims, now),
        signedBy: readSigner(one, changed.header, verifyKey),
      };
    });
}
