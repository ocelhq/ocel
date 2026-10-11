import { describe, expect, it } from "bun:test";
import { backendBucketChecks, immutableFileOf } from "./backendBucket";
import type { CheckContext, Fetch } from "./context";

describe("the immutable file a page links", () => {
  it("is the first script or stylesheet under the immutable prefix", () => {
    const html =
      '<link rel="stylesheet" href="/_next/static/css/app.css?dpl=1"/><script src="/_next/static/chunks/main-abc.js" async></script>';
    expect(immutableFileOf(html)).toBe("/_next/static/css/app.css");
  });

  it("is none when the page links nothing under it", () => {
    expect(immutableFileOf('<script src="/other/main.js"></script>')).toBeUndefined();
  });
});

function context(
  fetch: Fetch,
  countOriginRequests: (fragment: string) => Promise<number>,
): CheckContext {
  return {
    app: "web",
    baseUrl: "https://web.journey.test",
    greeting: "journey-hello",
    maxRequestBodyBytes: 1024,
    phase: "verify",
    notes: new Map(),
    fetch,
    reach: async (url) => url,
    readExposed: async () => "",
    countOriginRequests,
    runInEnvironment: async () => "",
    journeyNonce: "journey-nonce",
    projectDir: "/nowhere",
    tempDir: "/nowhere",
  };
}

const served: Fetch = async (input) => {
  const url = String(input);
  if (url.endsWith("/")) {
    return new Response('<script src="/_next/static/chunks/a.js"></script>', { status: 200 });
  }
  if (url.includes("missing-")) {
    return new Response("", { status: 404 });
  }
  return new Response("", {
    status: 200,
    headers: { "cache-control": "public, max-age=31536000, immutable" },
  });
};

describe("the backend bucket checks", () => {
  const [servesImmutable] = backendBucketChecks;

  it("pass when only the control request reaches Cloud Run", async () => {
    const logged = (fragment: string) =>
      Promise.resolve(fragment.startsWith("e2e-dynamic=") ? 1 : 0);

    await expect(servesImmutable?.run(context(served, logged))).resolves.toBeUndefined();
  });

  it("fail when Cloud Run logs the request for the immutable file", async () => {
    const logged = () => Promise.resolve(1);

    await expect(servesImmutable?.run(context(served, logged))).rejects.toThrow(
      /sent them to the service/,
    );
  });
});
