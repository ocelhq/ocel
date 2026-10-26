import { describe, expect, it } from "vitest";
import { resolveServe, type ServeFetch } from "../src/index";
import { parseRenderCall, RENDER_RELEASE_HEADER, type RenderRelease } from "../src/serve-key";
import type { ServingPorts } from "../src/serving";
import { answerEveryRecordWith, capturing, makeRecord } from "./origin-deps";
import { resolveBase } from "./resolve-base";

const CONTAINER = "https://r1---web-abc123-ew.a.run.app";

function servingInto(served: Request[]): ServingPorts {
  return {
    serve: () => ({
      fetch: async (request) => {
        served.push(request);
        return new Response("cached page", { headers: { "cf-cache-status": "HIT" } });
      },
      purge: async () => ({ ok: true }),
    }),
    queue: { raise: async () => {} },
    waitUntil: () => {},
  };
}

async function containerServe(framework: string, cachesContainers: boolean) {
  const record = makeRecord({ app: "web", framework, functionUrls: {}, origin: CONTAINER });
  const served: Request[] = [];
  const wire = capturing();
  const serve = (await resolveServe(
    {
      binding: answerEveryRecordWith(async () => ({ kind: "record", release: "r1", record })),
      slug: "p1",
      host: "shop.example.com",
      app: "web",
    },
    resolveBase({
      originFetchFor: () => wire.fetch,
      serving: servingInto(served),
      cachesContainers,
    }),
  )) as ServeFetch;
  return { serve, served, wire };
}

describe("a container's release no route table routes, behind a worker that caches containers", () => {
  it("is answered through Serve under the app's own cache headers", async () => {
    const { serve, served, wire } = await containerServe("node", true);

    const res = await serve(new Request("https://shop.example.com/catalog?page=2"));

    expect(await res.text()).toBe("cached page");
    expect(wire.calls).toHaveLength(0);
    expect(served).toHaveLength(1);
    expect(parseRenderCall(served[0].url).source).toEqual({
      kind: "origin",
      url: `${CONTAINER}/catalog?page=2`,
    });
    const release = JSON.parse(served[0].headers.get(RENDER_RELEASE_HEADER)!) as RenderRelease;
    expect(release.honoursAppCacheHeaders).toBe(true);
  });

  const passedThrough: [string, RequestInit & { url?: string }][] = [
    ["a protocol upgrade", { headers: { upgrade: "websocket" } }],
    ["an event stream", { headers: { accept: "text/event-stream" } }],
    ["a request carrying credentials", { headers: { authorization: "Bearer t" } }],
    ["the edge's liveness probe", { url: "https://shop.example.com/.well-known/ocel-edge" }],
    ["a plain http request", { url: "http://shop.example.com/" }],
  ];
  for (const [what, { url, ...init }] of passedThrough) {
    it(`passes ${what} straight to the container`, async () => {
      const { serve, served, wire } = await containerServe("node", true);

      await serve(new Request(url ?? "https://shop.example.com/", init));

      expect(served).toHaveLength(0);
      expect(wire.calls).toHaveLength(1);
    });
  }

  it("keeps a Next container built from its own image under its own cache headers, as any container", async () => {
    const { serve, served, wire } = await containerServe("next", true);

    await serve(new Request("https://shop.example.com/"));

    expect(served).toHaveLength(1);
    expect(wire.calls).toHaveLength(0);
  });

  it("passes every request straight through when the worker caches no containers", async () => {
    const { serve, served, wire } = await containerServe("node", false);

    await serve(new Request("https://shop.example.com/"));

    expect(served).toHaveLength(0);
    expect(wire.calls).toHaveLength(1);
  });
});
