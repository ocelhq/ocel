import { describe, expect, it } from "vitest";

import { authorized, bearer, constantTimeEqual } from "../src/auth";

describe("constantTimeEqual", () => {
  it("is true for identical strings", async () => {
    expect(await constantTimeEqual("secret", "secret")).toBe(true);
  });

  it("is false for different strings, including different lengths", async () => {
    expect(await constantTimeEqual("secret", "wrong")).toBe(false);
    expect(await constantTimeEqual("secret", "secret-but-longer")).toBe(false);
    expect(await constantTimeEqual("", "secret")).toBe(false);
  });
});

describe("authorized", () => {
  const req = (headers?: Record<string, string>) =>
    new Request("https://store.example/staged", { headers });

  it("accepts a correctly-signed bearer token", async () => {
    const request = req({ authorization: "Bearer the-secret" });
    expect(await authorized(request, "the-secret")).toBe(true);
  });

  it("rejects a missing authorization header", async () => {
    expect(await authorized(req(), "the-secret")).toBe(false);
  });

  it("rejects an incorrectly-signed bearer token", async () => {
    const request = req({ authorization: "Bearer wrong-secret" });
    expect(await authorized(request, "the-secret")).toBe(false);
  });

  it("rejects a non-bearer authorization header", async () => {
    const request = req({ authorization: "Basic dGhlLXNlY3JldA==" });
    expect(await authorized(request, "the-secret")).toBe(false);
  });
});

describe("bearer", () => {
  const req = (headers?: Record<string, string>) =>
    new Request("https://store.example/x", { headers });

  it("extracts the token from a bearer header", () => {
    expect(bearer(req({ authorization: "Bearer the-secret" }))).toBe("the-secret");
  });

  it("returns null when the header is missing or not a bearer", () => {
    expect(bearer(req())).toBeNull();
    expect(bearer(req({ authorization: "Basic dGhlLXNlY3JldA==" }))).toBeNull();
  });
});
