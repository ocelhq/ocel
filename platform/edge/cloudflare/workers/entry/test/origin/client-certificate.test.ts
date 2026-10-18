import { describe, expect, it } from "vitest";
import { clientCertificateOriginFetch } from "../../src/origin/client-certificate";

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

describe("clientCertificateOriginFetch", () => {
  it("reaches the origin through the client certificate binding", async () => {
    const { binding, sent } = fakeBinding();
    const originFetch = clientCertificateOriginFetch(binding);
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
    const originFetch = clientCertificateOriginFetch(binding);
    await withGlobalFetch(forbiddenFetch, () =>
      originFetch(new Request("https://d-abc.origin.example.com/x")),
    );
    expect(sent[0].headers.has("x-amz-date")).toBe(false);
    expect(sent[0].headers.has("x-amz-content-sha256")).toBe(false);
    expect(sent[0].headers.has("authorization")).toBe(false);
  });

  it("drops a client-authorization carrier the client sent on the client certificate path", async () => {
    const { binding, sent } = fakeBinding();
    const originFetch = clientCertificateOriginFetch(binding);
    await originFetch(
      new Request("https://d-abc.origin.example.com/x", {
        headers: { [CLIENT_AUTHORIZATION]: "forged" },
      }),
    );
    expect(sent[0].headers.has(CLIENT_AUTHORIZATION)).toBe(false);
  });

  it("passes a request body through the client certificate binding unchanged", async () => {
    const { binding, sent } = fakeBinding();
    const originFetch = clientCertificateOriginFetch(binding);
    await originFetch(
      new Request("https://d-abc.origin.example.com/x", {
        method: "POST",
        body: new Uint8Array([1, 2, 3, 0, 255]),
      }),
    );
    expect(sent[0].method).toBe("POST");
    expect([...new Uint8Array(await sent[0].arrayBuffer())]).toEqual([1, 2, 3, 0, 255]);
  });

  it("forwards a request body to the client certificate binding before the body has ended", async () => {
    let controller!: ReadableStreamDefaultController<Uint8Array>;
    const body = new ReadableStream<Uint8Array>({
      start(c) {
        controller = c;
        c.enqueue(new Uint8Array([7]));
      },
    });
    let firstChunk: number | undefined;
    const binding = {
      fetch: async (request: Request) => {
        const reader = request.body!.getReader();
        firstChunk = (await reader.read()).value?.[0];
        return new Response("ok");
      },
    } as unknown as Fetcher;
    const originFetch = clientCertificateOriginFetch(binding);
    const sending = originFetch(
      new Request("https://d-abc.origin.example.com/x", {
        method: "POST",
        body,
        duplex: "half",
      } as RequestInit),
    );
    const outcome = await Promise.race([
      sending.then(() => "forwarded"),
      new Promise((resolve) => setTimeout(() => resolve("buffering"), 200)),
    ]);
    controller.close();
    expect(outcome).toBe("forwarded");
    expect(firstChunk).toBe(7);
  });

  it("drops the empty-body sentinel on the client certificate path", async () => {
    const { binding } = fakeBinding(
      () => new Response("x", { headers: { "x-ocel-empty-body": "1" } }),
    );
    const originFetch = clientCertificateOriginFetch(binding);
    const response = await originFetch(new Request("https://d-abc.origin.example.com/x"));
    expect(response.headers.has("x-ocel-empty-body")).toBe(false);
    expect(await response.text()).toBe("");
  });

  it("refuses to send a request over the client certificate to a non-https origin", async () => {
    const { binding, sent } = fakeBinding();
    const originFetch = clientCertificateOriginFetch(binding);
    await expect(originFetch(new Request("http://d-abc.origin.example.com/x"))).rejects.toThrow(
      "ocel: refusing to present the client certificate over http:",
    );
    expect(sent).toHaveLength(0);
  });
});
