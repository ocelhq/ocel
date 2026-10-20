import { createExecutionContext } from "cloudflare:test";
import type { ImageOriginRequest } from "@framework/next-protocol/image";
import { describe, expect, it } from "vitest";
import type { Env } from "../src/env";
import { Serve } from "../src/serve";
import {
  buildImageCall,
  IMAGE_CALL_HEADER,
  RENDER_PROPS,
  RENDER_RELEASE_HEADER,
  SERVE_REFUSED_HEADER,
} from "../src/serve-key";
import { awsOrigin, FN_URL, withGlobalFetch } from "./origin-deps";

const storageCoordinate = "p1/prod/web/r1a2b3c4d";
const coordinates = { host: "shop.example.com", app: "web", release: "r1a2b3c4d" };
const DIGEST = "d".repeat(64);
const DEPLOYMENT_OPTIMIZER = `${FN_URL}_ocel/image`;
const ENV_OPTIMIZER = "https://opt.lambda-url.eu-west-2.on.aws/";

const PAYLOAD: ImageOriginRequest = {
  storage: { project: "p1", env: "prod", app: "web", release: "r1a2b3c4d" },
  url: "/a.png",
  w: 640,
  q: 75,
  accept: "image/webp",
  mimeType: "image/webp",
  configHash: "deadbeef",
};

const record = {
  app: "web",
  framework: "next",
  release: "build-1~fp1",
  buildId: "build-1",
  functionUrls: { "/": `${FN_URL}stage` },
  storageCoordinate,
  createdAt: 1,
  edge: {},
};

function serveWith(over: { served?: boolean; env?: Partial<Env> } = {}): Serve {
  const ctx = Object.assign(createExecutionContext(), { props: RENDER_PROPS });
  return new Serve(
    ctx as never,
    {
      RELEASES: {
        readServedRecord: async () =>
          over.served === false ? { kind: "not-served" } : { kind: "record", record },
      },
      OCEL_ORIGIN: awsOrigin(),
      ...over.env,
    } as unknown as Env,
  );
}

function imageRequest(
  optimizer = DEPLOYMENT_OPTIMIZER,
  over: { payload?: unknown; minimumCacheTTL?: unknown } = {},
): Request {
  const { url } = buildImageCall(optimizer, coordinates, DIGEST);
  return new Request(url, {
    headers: {
      [RENDER_RELEASE_HEADER]: JSON.stringify({ slug: "p1", storageCoordinate }),
      [IMAGE_CALL_HEADER]: JSON.stringify({
        payload: "payload" in over ? over.payload : PAYLOAD,
        minimumCacheTTL: "minimumCacheTTL" in over ? over.minimumCacheTTL : 14400,
      }),
    },
  });
}

function optimizerAnswering(answer: () => Response) {
  const calls: Request[] = [];
  const fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push(new Request(input as RequestInfo, init));
    return answer();
  }) as typeof globalThis.fetch;
  return { calls, fetch };
}

const webp = (headers: Record<string, string> = {}) =>
  new Response("optimized", {
    status: 200,
    headers: { "content-type": "image/webp", ...headers },
  });

describe("Serve optimizing an image", () => {
  it("posts the image's request to the release's own optimizer", async () => {
    const optimizer = optimizerAnswering(() => webp());

    await withGlobalFetch(optimizer.fetch, () => serveWith().fetch(imageRequest()));

    expect(optimizer.calls).toHaveLength(1);
    expect(optimizer.calls[0].method).toBe("POST");
    expect(optimizer.calls[0].url).toBe(DEPLOYMENT_OPTIMIZER);
    expect(await optimizer.calls[0].json()).toEqual(PAYLOAD);
  });

  it("keeps the image for the lifetime the visitor is told, then serves it stale while it refreshes", async () => {
    const optimizer = optimizerAnswering(() => webp({ vary: "accept" }));

    const response = await withGlobalFetch(optimizer.fetch, () =>
      serveWith().fetch(imageRequest()),
    );

    expect(response.status).toBe(200);
    expect(await response.text()).toBe("optimized");
    expect(response.headers.get("cloudflare-cdn-cache-control")).toBe(
      "max-age=14400, stale-while-revalidate=86400, stale-if-error=86400",
    );
    expect(response.headers.get("cache-control")).toBe("public, max-age=14400, must-revalidate");
    expect(response.headers.get("x-ocel-vary")).toBeNull();
    expect(response.headers.has("vary")).toBe(false);
  });

  it("tags the image with its release, so purging the release purges it", async () => {
    const optimizer = optimizerAnswering(() => webp());

    const response = await withGlobalFetch(optimizer.fetch, () =>
      serveWith().fetch(imageRequest()),
    );

    expect(response.headers.get("cache-tag")?.split(",")).toEqual([
      "r1a2b3c4d",
      "r1a2b3c4d|path:/_next/image",
    ]);
  });

  it("lifts the lifetime the optimizer declared over the build's minimum", async () => {
    const optimizer = optimizerAnswering(() => webp({ "cache-control": "public, max-age=86400" }));

    const response = await withGlobalFetch(optimizer.fetch, () =>
      serveWith().fetch(imageRequest()),
    );

    expect(response.headers.get("cloudflare-cdn-cache-control")).toMatch(/^max-age=86400,/);
  });

  it("never stores an image whose lifetime is nothing", async () => {
    const optimizer = optimizerAnswering(() => webp());

    const response = await withGlobalFetch(optimizer.fetch, () =>
      serveWith().fetch(imageRequest(DEPLOYMENT_OPTIMIZER, { minimumCacheTTL: 0 })),
    );

    expect(response.headers.get("cache-control")).toBe("private, no-store");
    expect(response.headers.get("cloudflare-cdn-cache-control")).toBeNull();
  });

  it("never stores what the optimizer refused or could not do", async () => {
    for (const status of [400, 500, 502]) {
      const optimizer = optimizerAnswering(
        () =>
          new Response("no", { status, headers: { "content-type": "text/plain; charset=utf-8" } }),
      );

      const response = await withGlobalFetch(optimizer.fetch, () =>
        serveWith().fetch(imageRequest()),
      );

      expect(response.status).toBe(status);
      expect(response.headers.get("cache-control")).toBe("private, no-store");
      expect(response.headers.get("cache-tag")).toBeNull();
    }
  });

  it("never stores an answer that is not an image", async () => {
    const optimizer = optimizerAnswering(
      () => new Response("<html>", { headers: { "content-type": "text/html" } }),
    );

    const response = await withGlobalFetch(optimizer.fetch, () =>
      serveWith().fetch(imageRequest()),
    );

    expect(response.status).toBe(502);
    expect(response.headers.get("cache-control")).toBe("private, no-store");
  });

  it("posts to the optimizer the bootstrap named when the call names it", async () => {
    const optimizer = optimizerAnswering(() => webp());

    const response = await withGlobalFetch(optimizer.fetch, () =>
      serveWith({ env: { OCEL_ORIGIN: awsOrigin({ optimizerUrl: ENV_OPTIMIZER }) } }).fetch(
        imageRequest(ENV_OPTIMIZER),
      ),
    );

    expect(response.status).toBe(200);
    expect(optimizer.calls[0].url).toBe(ENV_OPTIMIZER);
  });

  it("refuses an optimizer that is neither the bootstrap's nor one of the release's functions", async () => {
    const optimizer = optimizerAnswering(() => webp());

    for (const named of [ENV_OPTIMIZER, "https://evil.example/_ocel/image", `${FN_URL}other`]) {
      const response = await withGlobalFetch(optimizer.fetch, () =>
        serveWith().fetch(imageRequest(named)),
      );

      expect(response.status).toBe(503);
      expect(response.headers.get(SERVE_REFUSED_HEADER)).toBe("1");
    }
    expect(optimizer.calls).toHaveLength(0);
  });

  it("refuses a release that is not served", async () => {
    const optimizer = optimizerAnswering(() => webp());

    const response = await withGlobalFetch(optimizer.fetch, () =>
      serveWith({ served: false }).fetch(imageRequest()),
    );

    expect(response.headers.get(SERVE_REFUSED_HEADER)).toBe("1");
    expect(optimizer.calls).toHaveLength(0);
  });

  it("refuses a call that carries no image to optimize", async () => {
    const optimizer = optimizerAnswering(() => webp());

    for (const over of [{ payload: undefined }, { payload: { url: "/a.png" } }]) {
      const response = await withGlobalFetch(optimizer.fetch, () =>
        serveWith().fetch(imageRequest(DEPLOYMENT_OPTIMIZER, over)),
      );

      expect(response.headers.get(SERVE_REFUSED_HEADER)).toBe("1");
    }
    expect(optimizer.calls).toHaveLength(0);
  });
});
