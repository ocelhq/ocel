import { describe, expect, it } from "bun:test";
import { decodeToken, forgeRefusedCases, readTokenVectors } from "./tokenVectors";

const vectors = readTokenVectors();

const connect = {
  header: { alg: "EdDSA", typ: "JWT" },
  claims: {
    iss: "ocel:rt:app",
    aud: "127.0.0.1:40123",
    sub: "ada",
    iat: 1_800_000_000,
    exp: 1_800_000_060,
    jti: "live-jti",
    ocel: { op: "connect", ch: "/app", ns: "app" },
  },
};

function forged(name: string) {
  const found = forgeRefusedCases(vectors, connect).find((one) => one.name === name);
  if (!found) throw new Error(`no forgery named ${name}`);
  return found;
}

describe("the bad-token vectors replayed against a live token", () => {
  it("forges every case the vectors refuse, and none they accept", () => {
    expect(forgeRefusedCases(vectors, connect).map((one) => one.name)).toEqual(
      vectors.tokens.cases.filter((one) => !one.valid).map((one) => one.name),
    );
  });

  it("puts a changed time claim at the case's distance from the second the token is sent", () => {
    const sentAt = 1_800_000_100;
    expect(forged("expired").claimsAt(sentAt)).toMatchObject({
      iat: 1_800_000_030,
      exp: 1_800_000_090,
    });
    expect(forged("expiring at now").claimsAt(sentAt)).toMatchObject({
      iat: 1_800_000_040,
      exp: sentAt,
    });
  });

  it("makes a channel case's edit at the end of the live channel", () => {
    expect(forged("channel off by one character").claimsAt(connect.claims.iat).ocel).toEqual({
      op: "connect",
      ch: "/ap2",
      ns: "app",
    });
    expect(forged("channel one character longer").claimsAt(connect.claims.iat).ocel.ch).toBe(
      "/app0",
    );
  });

  it("changes only what the case changed, keeping every other live claim", () => {
    expect(forged("wrong aud").claimsAt(1_800_000_100)).toEqual({
      ...connect.claims,
      aud: "realtime.other.example",
    });
    expect(forged("wrong ns").claimsAt(1_800_000_100)).toEqual({
      ...connect.claims,
      ocel: { ...connect.claims.ocel, ns: "chat" },
    });
  });

  it("signs with another key or none where the case's signature does not verify", () => {
    expect(forged("wrong key").signedBy).toBe("another-key");
    expect(forged("alg none")).toMatchObject({ signedBy: "nobody", header: { alg: "none" } });
    expect(forged("wrong aud").signedBy).toBe("binding");
  });

  it("decodes the header and claims a token carries", () => {
    const valid = vectors.tokens.cases.find((one) => one.valid);
    const claims: Record<string, unknown> = decodeToken(valid?.token ?? "").claims;
    expect(claims).toEqual(vectors.tokens.mint[0]?.claims ?? {});
  });
});
