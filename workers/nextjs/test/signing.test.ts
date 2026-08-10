import { describe, expect, it } from "vitest";
import { awsServiceFetch, edgeOriginFetch, lambdaRegion } from "../src/signing";

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
    expect(auth).toContain("AWS4-HMAC-SHA256");
    expect(auth).toContain("/us-east-1/lambda/aws4_request");
    expect(signed?.headers.get("x-amz-date")).toBeTruthy();

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

    expect(signed?.method).toBe("POST");
    expect(await signed?.text()).toBe("POSTPONED");
    const auth = signed?.headers.get("authorization") ?? "";
    expect(auth).toContain("/us-east-1/lambda/aws4_request");
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

describe("awsServiceFetch", () => {
  async function capture(
    call: (send: ReturnType<typeof awsServiceFetch>) => Promise<unknown>,
  ): Promise<Request> {
    let sent: Request | undefined;
    const originalFetch = globalThis.fetch;
    globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
      sent = new Request(input as RequestInfo, init);
      return new Response("ok");
    }) as typeof fetch;
    try {
      await call(awsServiceFetch("AKIAEXAMPLE", "secretkey", "eu-west-2"));
    } finally {
      globalThis.fetch = originalFetch;
    }
    return sent!;
  }

  it("is undefined when a credential or the region is missing", () => {
    expect(awsServiceFetch(undefined, "s", "eu-west-2")).toBeUndefined();
    expect(awsServiceFetch("k", undefined, "eu-west-2")).toBeUndefined();
    expect(awsServiceFetch("k", "s", undefined)).toBeUndefined();
    expect(awsServiceFetch("k", "s", "")).toBeUndefined();
  });

  it("signs an S3 read against the bound region and the s3 service", async () => {
    const sent = await capture((send) =>
      send!("s3", "https://bucket.s3.eu-west-2.amazonaws.com/prod/p/a/b/x.json"),
    );
    const auth = sent.headers.get("authorization") ?? "";
    expect(auth).toContain("/eu-west-2/s3/aws4_request");
    expect(sent.headers.get("x-amz-content-sha256")).toBeTruthy();
  });

  it("signs a DynamoDB call's x-amz-target, which the API requires", async () => {
    const body = JSON.stringify({ TableName: "state" });
    const sent = await capture((send) =>
      send!("dynamodb", "https://dynamodb.eu-west-2.amazonaws.com/", {
        method: "POST",
        headers: {
          "content-type": "application/x-amz-json-1.0",
          "x-amz-target": "DynamoDB_20120810.UpdateItem",
        },
        body,
      }),
    );
    const auth = sent.headers.get("authorization") ?? "";
    expect(auth).toContain("/eu-west-2/dynamodb/aws4_request");
    const signedHeaders = /SignedHeaders=([^,]+)/.exec(auth)?.[1] ?? "";
    expect(signedHeaders).toContain("host");
    expect(signedHeaders).toContain("x-amz-target");
    expect(await sent.text()).toBe(body);
  });
});
