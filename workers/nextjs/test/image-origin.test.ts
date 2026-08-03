// Binding the substrate's image optimizer to the image route: what a worker does
// with OCEL_IMAGE_OPTIMIZER_URL, and — just as load-bearing — what it does
// without one. Nothing here reaches AWS; the signing fetch is the unit under test
// only in the sense that whatever fetch is handed in is the one used.
import { describe, expect, it } from "vitest";

import type { CacheDeps } from "../src/cache";
import {
  functionUrlImageOrigin,
  serveImage,
  unprovisionedImageOrigin,
  type ImageConfig,
  type ImageOriginRequest,
} from "../src/image";
import fixtures from "./fixtures/image-conformance.json";

const BASE_CONFIG = (fixtures.variants as unknown as Array<{ config: ImageConfig }>)[0]
  .config;

const OPTIMIZER_URL = "https://opt123.lambda-url.us-east-1.on.aws/";

const PAYLOAD: ImageOriginRequest = {
  slug: "acme",
  app: "web",
  buildId: "b1",
  url: "/a.png",
  w: 640,
  q: 75,
  accept: "image/avif,image/webp,*/*",
  mimeType: "image/avif",
  configHash: "deadbeef",
};

// Records what the worker asked its origin fetch to do. This stands in for
// edgeOriginFetch: the point of the test is that the optimizer is called through
// the *signing* fetch, whatever that fetch happens to be.
function recordingFetch(response: () => Response | Promise<Response>) {
  const calls: { url: string; init?: RequestInit }[] = [];
  const fn = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ url: String(input), init });
    return response();
  }) as typeof fetch;
  return { fetch: fn, calls };
}

// A CacheDeps over the real workerd cache, so the route takes the tier it takes
// in production rather than the uncached branch a missing cache falls to. Each
// caller names its own slug: the cache is process-wide and the key hashes the
// slug, so that is what keeps one test's entries out of another's.
function testCache(): CacheDeps {
  return { cache: caches.default, now: () => 0, waitUntil: () => {} };
}

// serveImage over a request the fixture config accepts, so the origin is
// actually reached rather than rejected at the edge. The cache is real: without
// one the route answers BYPASS, which is a property of the harness and not of
// anything a deployment does.
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
    slug,
    app: "web",
    buildId: "b1",
    origin,
    cache: testCache(),
  });
}

// What the optimizer answers a valid request with: bytes and the type it
// negotiated. The type is not decoration — it is what tells this hop that the
// optimizer wrote the 200 and not the Lambda runtime underneath it.
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
    // Verbatim, and the whole of what the origin is told: the optimizer reads no
    // request header, so every field it validates against has to be in the body.
    expect(JSON.parse(String(call.init?.body))).toEqual(PAYLOAD);
  });

  it("sends no client header to the optimizer", async () => {
    const recorded = recordingFetch(() => imageResponse());
    const origin = functionUrlImageOrigin(OPTIMIZER_URL, recorded.fetch)!;

    await origin(PAYLOAD);

    // content-type describes the JSON body this hop wrote. Anything else here
    // would be a header the edge forwarded — which is what CVE-2025-57752 was.
    const headers = new Headers(recorded.calls[0].init?.headers);
    expect([...headers.keys()]).toEqual(["content-type"]);
  });

  it("relays the optimizer's own statuses untouched", async () => {
    // 400, 500 and 502 are the whole of what the optimizer answers for itself,
    // and each body is Next's own message pinned by the conformance fixtures. The
    // caching tier above decides what to do with each status; this hop must not
    // rewrite any of it.
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
    // A Function URL the edge is not authorized for answers with AWS's own IAM
    // denial, which names the account, the region, the edge identity and the
    // function. This route is unauthenticated, and missing or rotated edge
    // credentials 403 every route — so the body must never reach a client.
    const denial = JSON.stringify({
      Message:
        "User: arn:aws:iam::123456789012:user/ocel-edge is not authorized to " +
        "perform: lambda:InvokeFunctionUrl on resource: " +
        "arn:aws:lambda:us-east-1:123456789012:function:ocel-bootstrap-ImageOptimizer-xYz",
    });
    const recorded = recordingFetch(() => new Response(denial, { status: 403 }));
    const origin = functionUrlImageOrigin(OPTIMIZER_URL, recorded.fetch)!;

    const response = await origin(PAYLOAD);

    // The substrate's own 502, which is also the status the colo tier refuses to
    // store — so nothing AWS wrote is cached either.
    expect(response.status).toBe(502);
    const body = await response.text();
    expect(body).not.toContain("123456789012");
    expect(body).not.toContain("arn:aws");
    expect(body).not.toContain("ocel-edge");
    expect(body).not.toContain("us-east-1");
  });

  it("discards every status the optimizer does not answer with", async () => {
    // Anything outside 200/400/500/502 was written by AWS rather than by the
    // optimizer: a throttle, a gateway timeout, a signing rejection. None of
    // those bodies are ours to relay.
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
    // The Function URL is RESPONSE_STREAM, which commits its status line before
    // the handler runs. A function that fails to initialise therefore answers
    // 200 — with Lambda's own JSON error payload, carrying errorType,
    // errorMessage and a stack trace naming /var/task and every bundled
    // dependency's version. A released artifact did exactly this, and status
    // alone cannot tell it from an image.
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
      // Casing and parameters are the origin's to write, not this hop's to
      // normalise.
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
    // The absent-env-var case. undefined is what keeps the route on
    // unprovisionedImageOrigin instead of on a call that cannot be made.
    expect(functionUrlImageOrigin(undefined, fetch)).toBeUndefined();
    expect(functionUrlImageOrigin("", fetch)).toBeUndefined();
  });

  it("is unbound when the configured URL is not a URL", () => {
    // Binding a malformed value would throw once per request, deep inside an
    // unauthenticated route. Not binding it degrades to the same 502 an
    // unprovisioned substrate serves.
    expect(functionUrlImageOrigin("opt123.lambda-url.us-east-1.on.aws", fetch))
      .toBeUndefined();
    expect(functionUrlImageOrigin("/_next/image", fetch)).toBeUndefined();
  });

  it("answers 502 rather than throwing when the call cannot be made", async () => {
    // The signing fetch throws on a host it cannot sign for, and a network
    // failure throws too. This route runs ahead of middleware on an
    // unauthenticated path, so a throw here is a Worker crash page.
    const origin = functionUrlImageOrigin(OPTIMIZER_URL, (() => {
      throw new Error("cannot sign request to non-Function-URL host");
    }) as unknown as typeof fetch)!;

    const response = await origin(PAYLOAD);

    expect(response.status).toBe(502);
  });
});

describe("the image route without an optimizer", () => {
  it("still answers a valid request 502, exactly as before one existed", async () => {
    // What every substrate gets until bootstrap provisions an optimizer: the
    // request is valid, so it is not a 400, and the substrate cannot answer it.
    // MISS, not BYPASS: the colo tier was consulted and had nothing, which is
    // what a deployment does with the same response.
    const origin = functionUrlImageOrigin(undefined, fetch);
    const response = await serveThrough(origin ?? unprovisionedImageOrigin, {
      slug: "no-optimizer",
    });

    expect(response.status).toBe(502);
    expect(response.headers.get("x-ocel-cache")).toBe("MISS");
    // Never stored, either: the request was well-formed and it is the substrate
    // that could not answer it.
    expect(response.headers.get("cache-control")).toBeNull();
  });

  // This is what makes the unpinned build this PR ships survivable. Both artifact
  // constants are empty, so every account it bootstraps 502s every image — and a
  // static import asks for `immutable` for ten years. Stamped on that 502, a
  // browser would pin it past any redeploy, re-bootstrap or purge, with no
  // revalidation to reach. The colo tier refuses to store a non-200 and cannot be
  // told about a browser's cache, so the header simply must not be written.
  it("puts no immutable Cache-Control on a static import it cannot serve", async () => {
    const staticImport = `url=${encodeURIComponent("/_next/static/media/logo.abc123.png")}&w=640&q=75`;

    for (const [name, status] of [
      // The unprovisioned substrate, and a rotated edge key: server-side these
      // differ, but a ten-year immutable browser entry would be identical.
      ["502", 502],
      ["403", 403],
    ] as const) {
      const response = await serveThrough(
        // Bare, like the optimizer's own errors and Next's: nothing on the way in
        // could account for a Cache-Control on the way out.
        async () => new Response(`upstream said ${status}`, { status }),
        {
          slug: `static-error-${name}`,
          search: staticImport,
          // The fixture config's localPatterns do not admit /_next/static/media,
          // and this test is about the header on the way out, not the allowlist.
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
    // The route hands the origin the identity the optimizer re-validates against.
    const sent = JSON.parse(String(recorded.calls[0].init?.body));
    expect(sent).toMatchObject({
      slug: "acme",
      app: "web",
      buildId: "b1",
      url: "/a.png",
      w: 640,
      q: 75,
      configHash: BASE_CONFIG.configHash,
    });
  });
});
