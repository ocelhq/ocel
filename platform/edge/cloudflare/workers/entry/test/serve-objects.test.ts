import { describe, expect, it } from "vitest";
import type { ServeProps } from "../src/serve-key";
import { serveObjects } from "../src/serve-objects";

const key = "prod/shop/web/r1a2b3c4d/route-table/abc.json";

function serving(answer: (request: Request) => Response) {
  const calls: { props: ServeProps; request: Request }[] = [];
  const serve = ({ props }: { props: ServeProps }) => ({
    fetch: async (request: Request) => {
      calls.push({ props, request });
      return answer(request);
    },
  });
  return { calls, serve };
}

describe("the object store that reads through Serve", () => {
  it("asks Serve for the key on behalf of the request's host", async () => {
    const { calls, serve } = serving(() => new Response('{"a":1}', { headers: { etag: '"e"' } }));

    const object = await serveObjects(serve, "shop.example.com").get(key);

    expect(calls).toHaveLength(1);
    expect(calls[0].props).toEqual({
      kind: "object",
      host: "shop.example.com",
      app: "web",
      release: "r1a2b3c4d",
    });
    expect(calls[0].request.method).toBe("GET");
    expect(new URL(calls[0].request.url).pathname).toBe(`/${key}`);
    expect(await object?.text()).toBe('{"a":1}');
  });

  it("hands the body and ETag to the asset reader", async () => {
    const { serve } = serving(() => new Response("bytes", { headers: { etag: '"e"' } }));

    const object = await serveObjects(serve, "h").get(key);

    expect(object?.httpEtag).toBe('"e"');
    expect(await new Response(object?.body).text()).toBe("bytes");
  });

  it("reads the bytes of an object as a buffer", async () => {
    const { serve } = serving(() => new Response(new Uint8Array([1, 2, 3])));

    const object = await serveObjects(serve, "h").get(key);

    expect(new Uint8Array((await object?.arrayBuffer()) ?? new ArrayBuffer(0))).toEqual(
      new Uint8Array([1, 2, 3]),
    );
  });

  it("reads a missing object as none", async () => {
    const { serve } = serving(() => new Response("Not Found", { status: 404 }));

    expect(await serveObjects(serve, "h").get(key)).toBeNull();
  });

  it("refuses to read a failure as an object", async () => {
    const { serve } = serving(() => new Response("no", { status: 502 }));

    await expect(serveObjects(serve, "h").get(key)).rejects.toThrow(/502/);
  });
});
