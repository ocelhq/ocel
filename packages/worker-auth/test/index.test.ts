import { describe, expect, it } from "vitest";

import { bearer, matchesHash, matchesSecret, sha256Hex, timingSafeEqual } from "../src/index.js";

const req = (headers?: Record<string, string>) =>
  new Request("https://worker.example/x", { headers });

describe("bearer", () => {
  it("extracts the token from a bearer header", () => {
    expect(bearer(req({ authorization: "Bearer the-secret" }))).toBe("the-secret");
  });

  it("returns null when the header is missing or not a bearer", () => {
    expect(bearer(req())).toBeNull();
    expect(bearer(req({ authorization: "Basic dGhlLXNlY3JldA==" }))).toBeNull();
  });
});

describe("sha256Hex", () => {
  it("is the lowercase hex SHA-256 digest", async () => {
    expect(await sha256Hex("abc")).toBe(
      "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
    );
  });
});

describe("timingSafeEqual", () => {
  it("is true only for identical strings", () => {
    expect(timingSafeEqual("a".repeat(64), "a".repeat(64))).toBe(true);
    expect(timingSafeEqual("a".repeat(64), "b".repeat(64))).toBe(false);
    expect(timingSafeEqual("a".repeat(64), "a".repeat(63))).toBe(false);
    expect(timingSafeEqual("", "")).toBe(true);
  });
});

describe("matchesSecret", () => {
  it("accepts the same plaintext and rejects anything else", async () => {
    expect(await matchesSecret("the-secret", "the-secret")).toBe(true);
    expect(await matchesSecret("wrong", "the-secret")).toBe(false);
    expect(await matchesSecret("", "the-secret")).toBe(false);
    expect(await matchesSecret("the-secret", "the-secret-but-longer")).toBe(false);
  });
});

describe("matchesHash", () => {
  it("accepts the token whose digest is the stored hash", async () => {
    const hash = await sha256Hex("write-secret");
    expect(await matchesHash("write-secret", hash)).toBe(true);
    expect(await matchesHash("other-secret", hash)).toBe(false);
  });

  it("rejects a token compared against a malformed hash", async () => {
    expect(await matchesHash("write-secret", "")).toBe(false);
  });
});
