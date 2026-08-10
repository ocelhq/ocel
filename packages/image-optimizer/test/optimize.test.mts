import http from "node:http";
import { createHash } from "node:crypto";
import type { AddressInfo } from "node:net";
import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { resetConfigMemo } from "../src/config.mjs";
import { IMAGE_PASSTHROUGH } from "../src/contract.mjs";
import { optimize } from "../src/optimize.mjs";
import { isReachableAddress } from "../src/addresses.mjs";
import type { UpstreamDeps } from "../src/upstream.mjs";
import { configHash, imageConfig, payload, storeWithConfig } from "./fixtures.mjs";
import { animatedGif, solid } from "./images.mjs";

const ASSET = "assets/proj1/web/build-1/logo.png";

beforeEach(() => resetConfigMemo());

const servers: http.Server[] = [];
afterEach(async () => {
  await Promise.all(
    servers.splice(0).map((s) => new Promise<void>((resolve) => s.close(() => resolve()))),
  );
});

function serve(handler: http.RequestListener): Promise<number> {
  const server = http.createServer(handler);
  servers.push(server);
  return new Promise((resolve) =>
    server.listen(0, "127.0.0.1", () => resolve((server.address() as AddressInfo).port)),
  );
}

const loopback: UpstreamDeps = {
  lookup: ((_h: string, _o: unknown, cb: Function) =>
    cb(null, [{ address: "127.0.0.1", family: 4 }])) as UpstreamDeps["lookup"],
  isReachable: (address: string) => address === "127.0.0.1" || isReachableAddress(address),
};

function text(body: Uint8Array): string {
  return new TextDecoder().decode(body);
}

describe("the config the edge validated against", () => {
  test("a digest mismatch is a 502 and nothing is fetched", async () => {
    const declared = imageConfig();
    const store = storeWithConfig(imageConfig({ domains: ["evil.example"] }));
    store.put(ASSET, { bytes: await solid("png") });
    const response = await optimize(payload(declared), { store });
    expect(response.status).toBe(502);
    expect(response.headers).toEqual({});
    expect(store.reads).toEqual(["image-config/proj1/web/build-1.json"]);
  });

  test("a missing config is a 502", async () => {
    const config = imageConfig();
    const store = storeWithConfig(config);
    store.objects.delete("image-config/proj1/web/build-1.json");
    expect((await optimize(payload(config), { store })).status).toBe(502);
  });

  test("a config for another build cannot be substituted", async () => {
    const config = imageConfig();
    const store = storeWithConfig(config);
    store.put("image-config/proj1/web/build-2.json", store.objects.get(
      "image-config/proj1/web/build-1.json",
    )!);
    store.objects.delete("image-config/proj1/web/build-1.json");
    expect((await optimize(payload(config), { store })).status).toBe(502);
  });

  test("an identifier that is not a single path segment is a 502", async () => {
    const config = imageConfig();
    const store = storeWithConfig(config);
    const response = await optimize(payload(config, { buildId: "../build-2" }), { store });
    expect(response.status).toBe(502);
    expect(store.reads).toEqual([]);
  });
});

describe("re-validation", () => {
  test("rejects a width the loaded config does not allow", async () => {
    const config = imageConfig();
    const store = storeWithConfig(config);
    const response = await optimize(payload(config, { w: 641 }), { store });
    expect(response.status).toBe(400);
    expect(text(response.body)).toBe('"w" parameter (width) of 641 is not allowed');
    expect(response.headers).toEqual({});
  });

  test("rejects a host the loaded config does not allow", async () => {
    const config = imageConfig();
    const store = storeWithConfig(config);
    const response = await optimize(
      payload(config, { url: "https://evil.example/a.png" }),
      { store },
    );
    expect(response.status).toBe(400);
    expect(text(response.body)).toBe('"url" parameter is not allowed');
  });

  test("a tightened config takes effect against a payload the old one admitted", async () => {
    const tightened = imageConfig({ deviceSizes: [1080], imageSizes: [] });
    const store = storeWithConfig(tightened);
    store.put(ASSET, { bytes: await solid("png") });
    const response = await optimize(
      { ...payload(tightened), w: 640, configHash: configHash(tightened) },
      { store },
    );
    expect(response.status).toBe(400);
    expect(text(response.body)).toBe('"w" parameter (width) of 640 is not allowed');
  });

  describe("the negotiated mimeType", () => {
    test("an arbitrary string is a 502, not a Content-Type", async () => {
      const config = imageConfig();
      const store = storeWithConfig(config);
      store.put(ASSET, { bytes: await solid("png") });
      const response = await optimize(payload(config, { mimeType: "text/html" }), { store });
      expect(response.status).toBe(502);
      expect(response.headers).toEqual({});
      expect(text(response.body)).toBe(
        "The image optimizer could not serve this request.",
      );
    });

    test("a format the loaded config does not list is refused", async () => {
      const config = imageConfig({ formats: ["image/webp"] });
      const store = storeWithConfig(config);
      store.put(ASSET, { bytes: await solid("png") });
      const response = await optimize(payload(config, { mimeType: "image/avif" }), { store });
      expect(response.status).toBe(502);
    });

    test("a config that does list it serves it", async () => {
      const config = imageConfig({ formats: ["image/avif", "image/webp"] });
      const store = storeWithConfig(config);
      store.put(ASSET, { bytes: await solid("png") });
      const response = await optimize(payload(config, { mimeType: "image/avif" }), { store });
      expect(response.status).toBe(200);
      expect(response.headers["content-type"]).toBe("image/avif");
    });

    test("an empty mimeType is no negotiation and keeps the source type", async () => {
      const config = imageConfig();
      const store = storeWithConfig(config);
      store.put(ASSET, { bytes: await solid("png") });
      const response = await optimize(payload(config, { mimeType: "" }), { store });
      expect(response.status).toBe(200);
      expect(response.headers["content-type"]).toBe("image/png");
    });
  });

  test("a url no parser can handle is a controlled 500", async () => {
    const config = imageConfig();
    const store = storeWithConfig(config);
    const response = await optimize(payload(config, { url: "/%" }), { store });
    expect(response.status).toBe(500);
    expect(text(response.body)).toBe("Internal Server Error");
  });
});

describe("a local image", () => {
  test("is read from the build's own asset prefix and transformed", async () => {
    const config = imageConfig();
    const store = storeWithConfig(config);
    store.put(ASSET, {
      bytes: await solid("png", 400, 200),
      cacheControl: "public, max-age=31536000, immutable",
      etag: '"stored-etag"',
    });
    const response = await optimize(payload(config, { mimeType: "image/webp" }), { store });

    expect(response.status).toBe(200);
    expect(store.reads).toContain(ASSET);
    expect(response.headers["content-type"]).toBe("image/webp");
    expect(response.headers["cache-control"]).toBe("public, max-age=31536000, immutable");
    expect(response.headers["etag"]).toBe(
      createHash("sha256").update(response.body).digest("base64url"),
    );
    expect(response.headers["content-disposition"]).toBe('attachment; filename="logo.webp"');
    expect(response.headers["content-security-policy"]).toBe(config.contentSecurityPolicy);
    expect(response.headers["x-content-type-options"]).toBe("nosniff");
    expect(response.headers[IMAGE_PASSTHROUGH]).toBeUndefined();
  });

  test("a path the build never emitted is a 400, not a 502", async () => {
    const config = imageConfig();
    const store = storeWithConfig(config);
    const response = await optimize(payload(config), { store });
    expect(response.status).toBe(400);
    expect(text(response.body)).toBe(
      '"url" parameter is valid but upstream response is invalid',
    );
  });

  test("is capped at maximumResponseBody like any other read", async () => {
    const config = imageConfig({ maximumResponseBody: 1024 });
    const store = storeWithConfig(config);
    store.put(ASSET, { bytes: await solid("png", 400, 400) });
    const response = await optimize(payload(config), { store });
    expect(response.status).toBe(400);
    expect(text(response.body)).toBe(
      '"url" parameter is valid but upstream response is invalid',
    );
  });

  test("a traversal in the path fails closed before any read", async () => {
    const config = imageConfig();
    const store = storeWithConfig(config);
    const response = await optimize(
      payload(config, { url: "/%2e%2e%2f%2e%2e%2fother/secret.png" }),
      { store },
    );
    expect(response.status).toBe(502);
    expect(store.reads).toEqual(["image-config/proj1/web/build-1.json"]);
  });

  test("no cache-control on the object means none on the response", async () => {
    const config = imageConfig();
    const store = storeWithConfig(config);
    store.put(ASSET, { bytes: await solid("png") });
    const response = await optimize(payload(config), { store });
    expect(response.headers["cache-control"]).toBeUndefined();
  });
});

describe("a remote image", () => {
  test("is fetched, transformed and its directives relayed", async () => {
    const config = imageConfig({
      remotePatterns: [
        { protocol: "http", hostname: "^cdn\\.example\\.com$", pathname: "^\\/.*$" },
      ],
    });
    const store = storeWithConfig(config);
    const image = await solid("jpeg", 400, 200);
    const port = await serve((_req, res) => {
      res.writeHead(200, {
        "content-type": "image/jpeg",
        "cache-control": "public, s-maxage=600",
      });
      res.end(Buffer.from(image));
    });
    const response = await optimize(
      payload(config, {
        url: `http://cdn.example.com:${port}/photo.jpg`,
        mimeType: "image/webp",
      }),
      { store, upstream: loopback },
    );
    expect(response.status).toBe(200);
    expect(response.headers["content-type"]).toBe("image/webp");
    expect(response.headers["cache-control"]).toBe("public, s-maxage=600");
    expect(response.headers["content-disposition"]).toBe('attachment; filename="photo.webp"');
  });
});

describe("failure behavior", () => {
  test("a transform failure serves the original bytes and marks them", async () => {
    const config = imageConfig();
    const store = storeWithConfig(config);
    const gif = new Uint8Array([
      0x47, 0x49, 0x46, 0x38, 0x39, 0x61, 0x01, 0x00, 0x01, 0x00, 0x80, 0x00, 0x00,
      0xff, 0xff, 0xff, 0x00, 0x00, 0x00, 0x2c, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00,
      0x01, 0x00, 0x00, 0x02, 0x02, 0x44, 0x01, 0x00, 0x3b,
    ]);
    store.put(ASSET, { bytes: gif, cacheControl: "public, max-age=999999", etag: '"src"' });
    const response = await optimize(payload(config), { store });

    expect(response.status).toBe(200);
    expect(Buffer.from(response.body).equals(Buffer.from(gif))).toBe(true);
    expect(response.headers["content-type"]).toBe("image/gif");
    expect(response.headers[IMAGE_PASSTHROUGH]).toBe("1");
    expect(response.headers["x-content-type-options"]).toBe("nosniff");
    expect(response.headers["etag"]).toBe(Buffer.from('"src"').toString("base64url"));
  });

  test("an animated image is unmodified but is not a passthrough", async () => {
    const config = imageConfig();
    const store = storeWithConfig(config);
    store.put(ASSET, { bytes: animatedGif(), cacheControl: "public, max-age=600" });
    const response = await optimize(payload(config), { store });
    expect(response.status).toBe(200);
    expect(response.headers["content-type"]).toBe("image/gif");
    expect(response.headers[IMAGE_PASSTHROUGH]).toBeUndefined();
    expect(response.headers["cache-control"]).toBe("public, max-age=600");
  });

  test("a payload that is not an image at all is a 400", async () => {
    const config = imageConfig();
    const store = storeWithConfig(config);
    store.put(ASSET, {
      bytes: new TextEncoder().encode("<!DOCTYPE html><script>alert(1)</script>"),
      cacheControl: null,
    });
    const response = await optimize(payload(config), { store });
    expect(response.status).toBe(400);
    expect(text(response.body)).toBe("The requested resource isn't a valid image.");
    expect(response.headers).toEqual({});
  });

  test("an SVG without the flag is a 400 even though it is a real image", async () => {
    const config = imageConfig();
    const store = storeWithConfig(config);
    store.put(ASSET, { bytes: new TextEncoder().encode("<svg onload=\"alert(1)\"/>") });
    const response = await optimize(payload(config), { store });
    expect(response.status).toBe(400);
    expect(text(response.body)).toBe('"url" parameter is valid but image type is not allowed');
  });

  test("a store that throws says nothing of the cause in the body", async () => {
    const config = imageConfig();
    const store = storeWithConfig(config);
    const response = await optimize(payload(config), {
      store: {
        get: async (key, limit) => {
          if (key === ASSET) throw new Error("AccessDenied: arn:aws:iam::1234:role/secret");
          return store.get(key, limit);
        },
      },
    });
    expect(response.status).toBe(400);
    expect(text(response.body)).not.toContain("arn:aws");
    expect(text(response.body)).toBe(
      '"url" parameter is valid but upstream response is invalid',
    );
  });
});
