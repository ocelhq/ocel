import { describe, expect, it } from "vitest";

import { authorized, bearer, matchesSecret } from "../src/auth";

const req = (headers?: Record<string, string>) =>
  new Request("https://store.example/staged", { headers });

describe("authorized", () => {
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
  it("extracts the token from a bearer header", () => {
    expect(bearer(req({ authorization: "Bearer the-secret" }))).toBe("the-secret");
  });

  it("returns null when the header is missing or not a bearer", () => {
    expect(bearer(req())).toBeNull();
    expect(bearer(req({ authorization: "Basic dGhlLXNlY3JldA==" }))).toBeNull();
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
