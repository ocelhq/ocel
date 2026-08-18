import { describe, expect, it } from "vitest";

import {
  getSupportedMimeType,
  isImageRequest,
  serveImage,
  validateImageRequest,
  type ImageConfig,
  type ImageOriginRequest,
} from "../src/image.mjs";
import fixtures from "../fixtures/image-conformance.json" with { type: "json" };

const ASSET_PREFIX = "prod/p1/web/r3f8a1c9d/assets";

const BASE_CONFIG = (fixtures.variants as unknown as Array<{ config: ImageConfig }>)[0]!
  .config;

function configWith(overrides: Partial<ImageConfig>): ImageConfig {
  return { ...BASE_CONFIG, ...overrides };
}

const ANY_PATHNAME = BASE_CONFIG.localPatterns![0]!.pathname;

function imageUrl(search: string, basePath = ""): URL {
  return new URL(`https://app.example${basePath}/_next/image?${search}`);
}

function validate(search: string, config: ImageConfig = BASE_CONFIG, accept = "") {
  return validateImageRequest(imageUrl(search), accept, config, "");
}

function rejection(search: string, config?: ImageConfig): string {
  const result = validate(search, config);
  if (result.ok) throw new Error(`expected ${search} to be rejected`);
  expect(result.status).toBe(400);
  return result.message;
}

const MALFORMED = {
  "a bare percent": "url=/%&w=640&q=75",
  "an encoded bare percent": "url=%2F%25&w=640&q=75",
  "an invalid escape sequence": "url=/a%zz&w=640&q=75",
  "a path the URL parser rejects": `url=${encodeURIComponent("/\\[/x.png")}&w=640&q=75`,
};

describe("validateImageRequest ordering", () => {
  it("rejects a protocol-relative url before it can reach the allowlist", () => {
    expect(rejection("url=//legacy.example/a.png&w=640&q=75")).toBe(
      '"url" parameter cannot be a protocol-relative URL (//)',
    );
    expect(
      rejection("url=//legacy.example/a.png&w=640&q=75", configWith({ localPatterns: undefined })),
    ).toBe('"url" parameter cannot be a protocol-relative URL (//)');
  });

  it("decodes the pathname before testing for recursion", () => {
    const encoded = encodeURIComponent("/_next/%69mage");
    expect(rejection(`url=${encoded}&w=640&q=75`)).toBe(
      '"url" parameter cannot be recursive',
    );
  });

  it("matches recursion anywhere in the pathname, not only as a prefix", () => {
    const prefixed = encodeURIComponent("/assets/cdn/_next/image/x.png");
    expect(rejection(`url=${prefixed}&w=640&q=75`)).toBe(
      '"url" parameter cannot be recursive',
    );
  });

  it("tests the integer regex before parseInt", () => {
    expect(rejection("url=/a.png&w=640.9&q=75")).toBe(
      '"w" parameter (width) must be an integer greater than 0',
    );
    expect(rejection("url=/a.png&w=640&q=75.9")).toBe(
      '"q" parameter (quality) must be an integer between 1 and 100',
    );
  });
});

describe("a url no runtime can parse", () => {
  for (const [name, search] of Object.entries(MALFORMED)) {
    it(`answers ${name} with a controlled 500`, async () => {
      const result = validate(search);
      expect(result.ok).toBe(false);
      expect(result.ok === false && result.status).toBe(500);

      const url = imageUrl(search);
      const response = await serveImage(new Request(url), url, {
        config: BASE_CONFIG,
        basePath: "",
        assetPrefix: ASSET_PREFIX,
        slug: "p1",
        deploymentId: "d1",
        app: "web",
        origin: async () => new Response("optimized"),
      });
      expect(response.status).toBe(500);
      expect(await response.text()).toBe("Internal Server Error");
    });
  }

  it("never reaches the origin", async () => {
    let called = false;
    for (const search of Object.values(MALFORMED)) {
      const url = imageUrl(search);
      await serveImage(new Request(url), url, {
        config: BASE_CONFIG,
        basePath: "",
        assetPrefix: ASSET_PREFIX,
        slug: "p1",
        deploymentId: "d1",
        app: "web",
        origin: async () => {
          called = true;
          return new Response(null);
        },
      });
    }
    expect(called).toBe(false);
  });

  it("is a 500 only where Next itself parses: an unparseable path with no localPatterns is allowed through", () => {
    const result = validate(
      MALFORMED["a path the URL parser rejects"],
      configWith({ localPatterns: undefined }),
    );
    expect(result.ok).toBe(true);
  });
});

describe("validateImageRequest pattern matching", () => {
  it("matches remote patterns against the hostname, not the host", () => {
    const withPort = validate(
      `url=${encodeURIComponent("https://cdn.allowed.example:8443/img/a.png")}&w=640&q=75`,
    );
    expect(withPort.ok).toBe(true);
  });

  it("reads userinfo as userinfo, so an allowlisted name cannot prefix a hostile host", () => {
    expect(
      rejection(
        `url=${encodeURIComponent("https://cdn.allowed.example@evil.example/img/a.png")}&w=640&q=75`,
      ),
    ).toBe('"url" parameter is not allowed');
  });

  it("rejects a host the pattern only suffixes", () => {
    expect(
      rejection(
        `url=${encodeURIComponent("https://allowed.example.evil.example/img/a.png")}&w=640&q=75`,
      ),
    ).toBe('"url" parameter is not allowed');
  });

  it("compares the protocol with the trailing colon stripped from both sides", () => {
    const config = configWith({
      remotePatterns: [{ protocol: "http", hostname: "^(?:cdn\\.example)$", pathname: ANY_PATHNAME }],
    });
    expect(
      validate(`url=${encodeURIComponent("http://cdn.example/a.png")}&w=640&q=75`, config).ok,
    ).toBe(true);
    expect(
      rejection(`url=${encodeURIComponent("https://cdn.example/a.png")}&w=640&q=75`, config),
    ).toBe('"url" parameter is not allowed');
  });
});

describe("validateImageRequest absent-key semantics", () => {
  it("treats absent localPatterns as allow-all and an empty array as deny-all", () => {
    expect(validate("url=/anything.png&w=640&q=75", configWith({ localPatterns: undefined })).ok).toBe(
      true,
    );
    expect(rejection("url=/anything.png&w=640&q=75", configWith({ localPatterns: [] }))).toBe(
      '"url" parameter is not allowed',
    );
  });

  it("treats absent qualities as any quality in 1..100 and an empty array as none", () => {
    const anyQuality = configWith({ qualities: undefined });
    expect(validate("url=/a.png&w=640&q=1", anyQuality).ok).toBe(true);
    expect(validate("url=/a.png&w=640&q=100", anyQuality).ok).toBe(true);
    expect(rejection("url=/a.png&w=640&q=101", anyQuality)).toBe(
      '"q" parameter (quality) must be an integer between 1 and 100',
    );
    expect(rejection("url=/a.png&w=640&q=75", configWith({ qualities: [] }))).toBe(
      '"q" parameter (quality) of 75 is not allowed',
    );
  });

  it("honours a localPattern that names no search, which allows any query string", () => {
    const config = configWith({ localPatterns: [{ pathname: ANY_PATHNAME }] });
    expect(validate("url=%2Fa.png%3Fv%3D1&w=640&q=75", config).ok).toBe(true);
    expect(rejection("url=%2Fa.png%3Fv%3D1&w=640&q=75")).toBe('"url" parameter is not allowed');
  });
});

describe("getSupportedMimeType", () => {
  it("negotiates nothing for a wildcard-only Accept", () => {
    expect(getSupportedMimeType(["image/webp"], "*/*")).toBe("");
    expect(getSupportedMimeType(["image/webp"], "image/*")).toBe("");
    expect(getSupportedMimeType(["image/webp"], "")).toBe("");
  });

  it("negotiates the configured format when the header names it literally", () => {
    expect(getSupportedMimeType(["image/webp"], "image/webp")).toBe("image/webp");
    expect(
      getSupportedMimeType(
        ["image/webp"],
        "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8",
      ),
    ).toBe("image/webp");
  });

  it("prefers the configured format's own order", () => {
    const accept = "image/avif,image/webp";
    expect(getSupportedMimeType(["image/avif", "image/webp"], accept)).toBe("image/avif");
    expect(getSupportedMimeType(["image/webp", "image/avif"], accept)).toBe("image/avif");
  });

  it("ignores a format the header excluded with q=0", () => {
    expect(getSupportedMimeType(["image/webp"], "image/webp;q=0,image/png")).toBe("");
  });
});

describe("isImageRequest", () => {
  it("is the route path under the app's basePath", () => {
    expect(isImageRequest("/_next/image", "")).toBe(true);
    expect(isImageRequest("/docs/_next/image", "/docs")).toBe(true);
    expect(isImageRequest("/_next/imagex", "")).toBe(true);
    expect(isImageRequest("/_next/static/media/a.png", "")).toBe(false);
  });

  it("also serves the unprefixed path, as Next does", () => {
    expect(isImageRequest("/_next/image", "/docs")).toBe(true);
  });

  it("strips basePath only on a segment boundary", () => {
    expect(isImageRequest("/docsx/_next/image", "/docs")).toBe(false);
  });
});

describe("serveImage", () => {
  it("hands the origin the validated parameters and the config hash", async () => {
    const seen: ImageOriginRequest[] = [];
    const url = imageUrl("url=%2Fa.png&w=640&q=75");
    const response = await serveImage(new Request(url, { headers: { accept: "image/webp" } }), url, {
      config: BASE_CONFIG,
      basePath: "",
      assetPrefix: ASSET_PREFIX,
      slug: "p1",
      deploymentId: "d1",
      app: "web",
      origin: async (payload) => {
        seen.push(payload);
        return new Response("optimized");
      },
    });

    expect(await response.text()).toBe("optimized");
    expect(seen).toEqual([
      {
        assetPrefix: ASSET_PREFIX,
        url: "/a.png",
        w: 640,
        q: 75,
        accept: "image/webp",
        mimeType: "image/webp",
        configHash: BASE_CONFIG.configHash,
      },
    ]);
  });

  it("sends the absolute url in its normalized form", async () => {
    const seen: ImageOriginRequest[] = [];
    const url = imageUrl(
      `url=${encodeURIComponent("https://cdn.allowed.example/img/../img/a.png")}&w=640&q=75`,
    );
    await serveImage(new Request(url), url, {
      config: BASE_CONFIG,
      basePath: "",
      assetPrefix: ASSET_PREFIX,
      slug: "p1",
      deploymentId: "d1",
      app: "web",
      origin: async (payload) => {
        seen.push(payload);
        return new Response(null);
      },
    });

    expect(seen[0]!.url).toBe("https://cdn.allowed.example/img/a.png");
  });

  it("never reaches the origin when validation fails", async () => {
    let called = false;
    const url = imageUrl("url=%2Fa.png&w=641&q=75");
    const response = await serveImage(new Request(url), url, {
      config: BASE_CONFIG,
      basePath: "",
      assetPrefix: ASSET_PREFIX,
      slug: "p1",
      deploymentId: "d1",
      app: "web",
      origin: async () => {
        called = true;
        return new Response(null);
      },
    });

    expect(called).toBe(false);
    expect(response.status).toBe(400);
    expect(await response.text()).toBe('"w" parameter (width) of 641 is not allowed');
  });

  it("serves the origin's bytes marked BYPASS when no host bound an image cache", async () => {
    const url = imageUrl("url=%2Fa.png&w=640&q=75");
    const response = await serveImage(new Request(url), url, {
      config: BASE_CONFIG,
      basePath: "",
      assetPrefix: ASSET_PREFIX,
      slug: "p1",
      deploymentId: "d1",
      app: "web",
      origin: async () => new Response("optimized"),
    });

    expect(response.headers.get("x-ocel-cache")).toBe("BYPASS");
    expect(await response.text()).toBe("optimized");
  });

  it("immutably caches an uncached static import and answers HEAD without a body", async () => {
    const url = imageUrl("url=%2F_next%2Fstatic%2Fmedia%2Fa.png&w=640&q=75");
    const serve = (method: string) =>
      serveImage(new Request(url, { method }), url, {
        config: configWith({ localPatterns: undefined }),
        basePath: "",
        assetPrefix: ASSET_PREFIX,
        slug: "p1",
        deploymentId: "d1",
        app: "web",
        origin: async () => new Response("optimized"),
      });

    const got = await serve("GET");
    expect(got.headers.get("cache-control")).toBe(
      "public, max-age=315360000, immutable",
    );

    const head = await serve("HEAD");
    expect(head.status).toBe(200);
    expect(head.body).toBe(null);
  });

  it("marks a /_next/static/media source as a static import", () => {
    const result = validateImageRequest(
      imageUrl("url=%2Fdocs%2F_next%2Fstatic%2Fmedia%2Fa.png&w=640&q=75"),
      "",
      configWith({ localPatterns: undefined }),
      "/docs",
    );
    expect(result.ok && result.params.isStatic).toBe(true);

    const unprefixed = validateImageRequest(
      imageUrl("url=%2F_next%2Fstatic%2Fmedia%2Fa.png&w=640&q=75"),
      "",
      configWith({ localPatterns: undefined }),
      "/docs",
    );
    expect(unprefixed.ok && unprefixed.params.isStatic).toBe(false);
  });
});
