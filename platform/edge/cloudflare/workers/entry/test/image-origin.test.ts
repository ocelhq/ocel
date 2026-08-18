import { describe, expect, it } from "vitest";

import type { CacheDeps } from "../src/cache";
import {
  functionUrlImageOrigin,
  unprovisionedImageOrigin,
  type ImageConfig,
  type ImageOriginRequest,
} from "@framework/next-router/image";
import { serveImage } from "../src/image";
import fixtures from "@framework/next-router/fixtures/image-conformance.json";
import { coloDeps } from "./cache-deps";

const BASE_CONFIG = (fixtures.variants as unknown as Array<{ config: ImageConfig }>)[0]
  .config;

const OPTIMIZER_URL = "https://opt123.lambda-url.us-east-1.on.aws/";

const ASSET_PREFIX = "prod/acme/web/r3f8a1c9d/assets";

const PAYLOAD: ImageOriginRequest = {
  assetPrefix: ASSET_PREFIX,
  url: "/a.png",
  w: 640,
  q: 75,
  accept: "image/avif,image/webp,*/*",
  mimeType: "image/avif",
  configHash: "deadbeef",
};

function recordingFetch(response: () => Response | Promise<Response>) {
  const calls: { url: string; init?: RequestInit }[] = [];
  const fn = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ url: String(input), init });
    return response();
  }) as typeof fetch;
  return { fetch: fn, calls };
}

function testCache(): CacheDeps {
  return coloDeps({ cache: caches.default, now: () => 0, waitUntil: () => {} });
}

function serveThrough(
  origin = unprovisionedImageOrigin,
  {
    slug = "acme",
    search = "url=%2Fa.png&w=640&q=75",
    config = BASE_CONFIG,
  }: { slug?: string; search?: string; config?: ImageConfig } = {},
) {
  const url = new URL(`https://app.example/_next/image?${search}`);
  return serveImage(new Request(url, { headers: { accept: "image/webp" } }), url, {
    config,
    basePath: "",
    assetPrefix: ASSET_PREFIX,
    slug,
    buildId: "b1",
    origin,
    cache: testCache(),
  });
}

function imageResponse() {
  return new Response("bytes", {
    status: 200,
    headers: { "content-type": "image/webp" },
  });
}

describe("functionUrlImageOrigin", () => {
  it("POSTs the validated request as JSON through the signing fetch", async () => {
    const recorded = recordingFetch(() => imageResponse());
    const origin = functionUrlImageOrigin(OPTIMIZER_URL, recorded.fetch)!;

    const response = await origin(PAYLOAD);

    expect(response.status).toBe(200);
    expect(await response.text()).toBe("bytes");
    expect(recorded.calls).toHaveLength(1);
    const [call] = recorded.calls;
    expect(call.url).toBe(OPTIMIZER_URL);
    expect(call.init?.method).toBe("POST");
    expect(new Headers(call.init?.headers).get("content-type")).toBe(
      "application/json",
    );
    expect(JSON.parse(String(call.init?.body))).toEqual(PAYLOAD);
  });

  it("sends no client header to the optimizer", async () => {
    const recorded = recordingFetch(() => imageResponse());
    const origin = functionUrlImageOrigin(OPTIMIZER_URL, recorded.fetch)!;

    await origin(PAYLOAD);

    const headers = new Headers(recorded.calls[0].init?.headers);
    expect([...headers.keys()]).toEqual(["content-type"]);
  });

  it("relays the optimizer's own statuses untouched", async () => {
    for (const status of [400, 500, 502]) {
      const recorded = recordingFetch(
        () => new Response(`body ${status}`, { status }),
      );
      const origin = functionUrlImageOrigin(OPTIMIZER_URL, recorded.fetch)!;
      const response = await origin(PAYLOAD);
      expect(response.status).toBe(status);
      expect(await response.text()).toBe(`body ${status}`);
    }
  });

  it("leaks nothing when AWS refuses the invocation", async () => {
    const denial = JSON.stringify({
      Message:
        "User: arn:aws:iam::123456789012:user/ocel-edge is not authorized to " +
        "perform: lambda:InvokeFunctionUrl on resource: " +
        "arn:aws:lambda:us-east-1:123456789012:function:ocel-bootstrap-ImageOptimizer-xYz",
    });
    const recorded = recordingFetch(() => new Response(denial, { status: 403 }));
    const origin = functionUrlImageOrigin(OPTIMIZER_URL, recorded.fetch)!;

    const response = await origin(PAYLOAD);

    expect(response.status).toBe(502);
    const body = await response.text();
    expect(body).not.toContain("123456789012");
    expect(body).not.toContain("arn:aws");
    expect(body).not.toContain("ocel-edge");
    expect(body).not.toContain("us-east-1");
  });

  it("discards every status the optimizer does not answer with", async () => {
    for (const status of [401, 403, 404, 413, 429, 503, 504]) {
      const recorded = recordingFetch(
        () => new Response(`aws internals for ${status}`, { status }),
      );
      const origin = functionUrlImageOrigin(OPTIMIZER_URL, recorded.fetch)!;
      const response = await origin(PAYLOAD);
      expect(response.status).toBe(502);
      expect(await response.text()).not.toContain("aws internals");
    }
  });

  it("discards a 200 that is not an image", async () => {
    const crash = JSON.stringify({
      errorType: "Error",
      errorMessage: 'Dynamic require of "node:https" is not supported',
      trace: ["    at file:///var/task/index.mjs:11:9"],
    });
    const recorded = recordingFetch(
      () =>
        new Response(crash, {
          status: 200,
          headers: { "content-type": "application/json" },
        }),
    );
    const origin = functionUrlImageOrigin(OPTIMIZER_URL, recorded.fetch)!;

    const response = await origin(PAYLOAD);

    expect(response.status).toBe(502);
    const body = await response.text();
    expect(body).not.toContain("/var/task");
    expect(body).not.toContain("errorMessage");
  });

  it("relays every image type the optimizer can negotiate", async () => {
    for (const type of [
      "image/avif",
      "image/webp",
      "image/png",
      "image/jpeg",
      "image/gif",
      "image/svg+xml",
      "IMAGE/WEBP",
      "image/webp; charset=binary",
    ]) {
      const recorded = recordingFetch(
        () => new Response("bytes", { status: 200, headers: { "content-type": type } }),
      );
      const origin = functionUrlImageOrigin(OPTIMIZER_URL, recorded.fetch)!;
      const response = await origin(PAYLOAD);
      expect(response.status).toBe(200);
      expect(await response.text()).toBe("bytes");
    }
  });

  it("is unbound when the substrate named no optimizer", () => {
    expect(functionUrlImageOrigin(undefined, fetch)).toBeUndefined();
    expect(functionUrlImageOrigin("", fetch)).toBeUndefined();
  });

  it("is unbound when the configured URL is not a URL", () => {
    expect(functionUrlImageOrigin("opt123.lambda-url.us-east-1.on.aws", fetch))
      .toBeUndefined();
    expect(functionUrlImageOrigin("/_next/image", fetch)).toBeUndefined();
  });

  it("answers 502 rather than throwing when the call cannot be made", async () => {
    const origin = functionUrlImageOrigin(OPTIMIZER_URL, (() => {
      throw new Error("cannot sign request to non-Function-URL host");
    }) as unknown as typeof fetch)!;

    const response = await origin(PAYLOAD);

    expect(response.status).toBe(502);
  });
});

describe("the image route without an optimizer", () => {
  it("still answers a valid request 502, exactly as before one existed", async () => {
    const origin = functionUrlImageOrigin(undefined, fetch);
    const response = await serveThrough(origin ?? unprovisionedImageOrigin, {
      slug: "no-optimizer",
    });

    expect(response.status).toBe(502);
    expect(response.headers.get("x-ocel-cache")).toBe("MISS");
    expect(response.headers.get("cache-control")).toBeNull();
  });

  it("puts no immutable Cache-Control on a static import it cannot serve", async () => {
    const staticImport = `url=${encodeURIComponent("/_next/static/media/logo.abc123.png")}&w=640&q=75`;

    for (const [name, status] of [
      ["502", 502],
      ["403", 403],
    ] as const) {
      const response = await serveThrough(
        async () => new Response(`upstream said ${status}`, { status }),
        {
          slug: `static-error-${name}`,
          search: staticImport,
          config: { ...BASE_CONFIG, localPatterns: undefined },
        },
      );

      expect(response.status).toBe(status);
      expect(response.headers.get("cache-control")).toBeNull();
    }
  });

  it("serves the optimizer's bytes once one is bound", async () => {
    const recorded = recordingFetch(
      () =>
        new Response("optimized", {
          status: 200,
          headers: { "content-type": "image/webp" },
        }),
    );
    const origin = functionUrlImageOrigin(OPTIMIZER_URL, recorded.fetch);

    const response = await serveThrough(origin ?? unprovisionedImageOrigin);

    expect(response.status).toBe(200);
    expect(await response.text()).toBe("optimized");
    expect(recorded.calls).toHaveLength(1);
    const sent = JSON.parse(String(recorded.calls[0].init?.body));
    expect(sent).toMatchObject({
      assetPrefix: ASSET_PREFIX,
      url: "/a.png",
      w: 640,
      q: 75,
      configHash: BASE_CONFIG.configHash,
    });
  });
});
