import { describe, expect, it } from "vitest";
import { originFetchFor } from "../src/origin-fetch";

const CLIENT_AUTHORIZATION = "x-ocel-client-authorization";

function fakeBinding(answer: () => Response = () => new Response("ok")) {
  const sent: Request[] = [];
  const binding = {
    fetch: async (request: Request) => {
      sent.push(request);
      return answer();
    },
  } as unknown as Fetcher;
  return { binding, sent };
}

async function withGlobalFetch<T>(stub: typeof fetch, run: () => Promise<T>): Promise<T> {
  const originalFetch = globalThis.fetch;
  globalThis.fetch = stub;
  try {
    return await run();
  } finally {
    globalThis.fetch = originalFetch;
  }
}

const forbiddenFetch = (async () => {
  throw new Error("global fetch must not be called");
}) as typeof fetch;

describe("originFetchFor", () => {
  it("reaches the origin through the client certificate binding when the worker holds no AWS keys", async () => {
    const { binding, sent } = fakeBinding();
    const originFetch = originFetchFor({ OCEL_ORIGIN_CLIENT_CERTIFICATE: binding })!;
    await withGlobalFetch(forbiddenFetch, () =>
      originFetch(
        new Request("https://d-abc.origin.example.com/x", {
          headers: { authorization: "Bearer t" },
        }),
      ),
    );
    expect(sent).toHaveLength(1);
    expect(sent[0].url).toBe("https://d-abc.origin.example.com/x");
    expect(sent[0].method).toBe("GET");
    expect(sent[0].redirect).toBe("manual");
    expect(sent[0].headers.get("authorization")).toBe("Bearer t");
  });

  it("adds no AWS signature to a request it sends through the client certificate binding", async () => {
    const { binding, sent } = fakeBinding();
    const originFetch = originFetchFor({ OCEL_ORIGIN_CLIENT_CERTIFICATE: binding })!;
    await withGlobalFetch(forbiddenFetch, () =>
      originFetch(new Request("https://d-abc.origin.example.com/x")),
    );
    expect(sent[0].headers.has("x-amz-date")).toBe(false);
    expect(sent[0].headers.has("x-amz-content-sha256")).toBe(false);
    expect(sent[0].headers.has("authorization")).toBe(false);
  });

  it("drops a client-authorization carrier the client sent on the client certificate path", async () => {
    const { binding, sent } = fakeBinding();
    const originFetch = originFetchFor({ OCEL_ORIGIN_CLIENT_CERTIFICATE: binding })!;
    await originFetch(
      new Request("https://d-abc.origin.example.com/x", {
        headers: { [CLIENT_AUTHORIZATION]: "forged" },
      }),
    );
    expect(sent[0].headers.has(CLIENT_AUTHORIZATION)).toBe(false);
  });

  it("passes a request body through the client certificate binding unchanged", async () => {
    const { binding, sent } = fakeBinding();
    const originFetch = originFetchFor({ OCEL_ORIGIN_CLIENT_CERTIFICATE: binding })!;
    await originFetch(
      new Request("https://d-abc.origin.example.com/x", {
        method: "POST",
        body: new Uint8Array([1, 2, 3, 0, 255]),
      }),
    );
    expect(sent[0].method).toBe("POST");
    expect([...new Uint8Array(await sent[0].arrayBuffer())]).toEqual([1, 2, 3, 0, 255]);
  });

  it("drops the empty-body sentinel on the client certificate path", async () => {
    const { binding } = fakeBinding(
      () => new Response("x", { headers: { "x-ocel-empty-body": "1" } }),
    );
    const originFetch = originFetchFor({ OCEL_ORIGIN_CLIENT_CERTIFICATE: binding })!;
    const response = await originFetch(new Request("https://d-abc.origin.example.com/x"));
    expect(response.headers.has("x-ocel-empty-body")).toBe(false);
    expect(await response.text()).toBe("");
  });

  it("signs with SigV4 when the worker holds AWS keys", async () => {
    let sent: Request | undefined;
    const stub = (async (input: RequestInfo | URL, init?: RequestInit) => {
      sent = new Request(input as RequestInfo, init);
      return new Response("ok");
    }) as typeof fetch;
    const originFetch = originFetchFor({
      OCEL_EDGE_ACCESS_KEY_ID: "AKIAEXAMPLE",
      OCEL_EDGE_SECRET_KEY: "secretkey",
    })!;
    await withGlobalFetch(stub, () =>
      originFetch(new Request("https://abc123.lambda-url.us-east-1.on.aws/x")),
    );
    expect(sent!.headers.get("authorization")).toMatch(/^AWS4-HMAC-SHA256/);
  });

  it("refuses a worker bound to both AWS keys and a client certificate", () => {
    const { binding } = fakeBinding();
    expect(() =>
      originFetchFor({
        OCEL_EDGE_ACCESS_KEY_ID: "AKIAEXAMPLE",
        OCEL_EDGE_SECRET_KEY: "secretkey",
        OCEL_ORIGIN_CLIENT_CERTIFICATE: binding,
      }),
    ).toThrow(
      "ocel: this worker is bound to both AWS signing keys and a client certificate; an origin is reached one way",
    );
  });

  it("is undefined when the worker holds neither keys nor a client certificate", () => {
    expect(originFetchFor({})).toBeUndefined();
  });
});
