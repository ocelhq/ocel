import { describe, expect, it } from "vitest";

import { authorized, constantTimeEqual } from "../src/auth";

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
