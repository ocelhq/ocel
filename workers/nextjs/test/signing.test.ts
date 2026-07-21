import { describe, expect, it } from "vitest";
import { edgeOriginFetch, lambdaRegion } from "../src/signing";

describe("lambdaRegion", () => {
  it("parses the region out of a Function URL host", () => {
    expect(lambdaRegion("abc123.lambda-url.us-east-1.on.aws")).toBe("us-east-1");
    expect(lambdaRegion("abc123.lambda-url.eu-west-2.on.aws")).toBe("eu-west-2");
  });

  it("returns undefined for a host that is not a Function URL", () => {
    expect(lambdaRegion("fn.example.com")).toBeUndefined();
    expect(lambdaRegion("lambda-url")).toBeUndefined();
  });
});

describe("edgeOriginFetch", () => {
  it("is undefined when either credential is missing", () => {
    expect(edgeOriginFetch(undefined, "s")).toBeUndefined();
    expect(edgeOriginFetch("k", undefined)).toBeUndefined();
    expect(edgeOriginFetch("", "")).toBeUndefined();
  });

  it("signs the forwarded request with SigV4 against the URL's region", async () => {
    let signed: Request | undefined;
    const originalFetch = globalThis.fetch;
    globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
      signed = new Request(input as RequestInfo, init);
      return new Response("ok");
    }) as typeof fetch;
    try {
      const origin = edgeOriginFetch("AKIAEXAMPLE", "secretkey")!;
      expect(origin).toBeDefined();
      await origin(
        new Request("https://abc123.lambda-url.us-east-1.on.aws/api/x", {
          method: "GET",
          headers: { cookie: "session=abc", "accept-encoding": "gzip" },
        }),
      );
    } finally {
      globalThis.fetch = originalFetch;
    }

    const auth = signed?.headers.get("authorization") ?? "";
    // SigV4 stamps the credential scope with the region parsed from the host and
    // the lambda service, and signs against the function URL's host.
    expect(auth).toContain("AWS4-HMAC-SHA256");
    expect(auth).toContain("/us-east-1/lambda/aws4_request");
    expect(signed?.headers.get("x-amz-date")).toBeTruthy();

    // Only host + the amz headers are signed; the forwarded app headers ride
    // along on the request but are never part of SignedHeaders, so Cloudflare
    // rewriting one (e.g. accept-encoding) in transit cannot break the signature.
    const signedHeaders =
      /SignedHeaders=([^,]+)/.exec(auth)?.[1] ?? "";
    expect(signedHeaders).toContain("host");
    expect(signedHeaders).not.toContain("cookie");
    expect(signedHeaders).not.toContain("accept-encoding");
    expect(signed?.headers.get("cookie")).toBe("session=abc");
  });

  it("signs a POST body (the PPR resume shape), forwarding method and body intact", async () => {
    let signed: Request | undefined;
    const originalFetch = globalThis.fetch;
    globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
      signed = new Request(input as RequestInfo, init);
      return new Response("ok");
    }) as typeof fetch;
    try {
      const origin = edgeOriginFetch("AKIAEXAMPLE", "secretkey")!;
      await origin(
        new Request("https://abc123.lambda-url.us-east-1.on.aws/resume", {
          method: "POST",
          headers: { "next-resume": "1", "content-type": "text/plain;charset=UTF-8" },
          body: "POSTPONED",
        }),
      );
    } finally {
      globalThis.fetch = originalFetch;
    }

    // The body must reach the origin verbatim: the signature covers its hash, so a
    // re-streamed or mutated body would 403 at the Function URL.
    expect(signed?.method).toBe("POST");
    expect(await signed?.text()).toBe("POSTPONED");
    const auth = signed?.headers.get("authorization") ?? "";
    expect(auth).toContain("/us-east-1/lambda/aws4_request");
    // The app's own headers ride along unsigned, exactly as on the GET path.
    const signedHeaders = /SignedHeaders=([^,]+)/.exec(auth)?.[1] ?? "";
    expect(signedHeaders).toContain("host");
    expect(signedHeaders).not.toContain("next-resume");
    expect(signed?.headers.get("next-resume")).toBe("1");
  });

  it("fails loudly rather than mis-signing a non-Function-URL host", async () => {
    const origin = edgeOriginFetch("AKIAEXAMPLE", "secretkey")!;
    await expect(
      origin(new Request("https://fn.example.com/x")),
    ).rejects.toThrow(/non-Function-URL host/);
  });
});
