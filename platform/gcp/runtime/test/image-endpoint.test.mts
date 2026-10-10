import http from "node:http";
import type { AddressInfo } from "node:net";
import {
  configHash,
  imageConfig,
  serialize,
} from "@framework/next-image-optimizer/test-support/fixtures";
import { solid } from "@framework/next-image-optimizer/test-support/images";
import type { Invoke } from "@framework/node-runtime/host";
import sharp from "sharp";
import { afterAll, beforeAll, expect, test } from "vitest";
import { newImageEndpointInvoke } from "../src/next/image-endpoint.mjs";
import { newCloudStorageBucket } from "./cloud-storage-bucket.mjs";
import { type ServedBucket, serveBucket } from "./serve-bucket.mjs";

const assetPrefix = "prod/shop/web/r1/assets";

const bucket = newCloudStorageBucket();
let stored: ServedBucket;
let origin: string;
let server: http.Server;
const handedToNext: string[] = [];

beforeAll(async () => {
  bucket.objects.set(`assets/${assetPrefix}/logo.png`, {
    body: await solid("png", 400, 200),
    generation: "1",
  });
  bucket.objects.set("assets/prod/shop/web/r1/image-config.json", {
    body: serialize(imageConfig()),
    generation: "1",
  });
  stored = await serveBucket(bucket);
  const env = {
    OCEL_ASSET_BUCKET: "ocel-acme-production",
    OCEL_STORAGE_ENDPOINT: stored.endpoint,
    OCEL_IMAGE_ENDPOINT: "1",
  };

  const next: Invoke = (req, res) => {
    handedToNext.push(`${req.method} ${req.url}`);
    res.writeHead(200, { "content-type": "text/plain" }).end("next");
  };
  const invoke = newImageEndpointInvoke(next, env);
  server = http.createServer((req, res) => {
    void Promise.resolve(invoke(req, res, {} as never)).catch(() => res.destroy());
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  origin = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
});

afterAll(async () => {
  server.close();
  await stored.close();
});

function payload(over: Record<string, unknown> = {}) {
  return {
    assetPrefix,
    url: "/logo.png",
    w: 64,
    q: 75,
    accept: "image/webp,*/*",
    mimeType: "image/webp",
    configHash: configHash(imageConfig()),
    ...over,
  };
}

test("the GCP service optimizes an image the edge posts to its origin path", async () => {
  const response = await fetch(`${origin}/_ocel/image`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(payload()),
  });

  expect(response.status).toBe(200);
  expect(response.headers.get("content-type")).toBe("image/webp");
  const resized = await sharp(Buffer.from(await response.arrayBuffer())).metadata();
  expect(resized).toMatchObject({ format: "webp", width: 64 });
});

test("the GCP service refuses any other method on its image path", async () => {
  const response = await fetch(`${origin}/_ocel/image`);

  expect(response.status).toBe(405);
  expect(response.headers.get("allow")).toBe("POST");
});

test("the GCP service refuses an image request that is not an object", async () => {
  for (const body of ["[]", "null", "not json", '"text"']) {
    const response = await fetch(`${origin}/_ocel/image`, { method: "POST", body });
    expect(response.status, body).toBe(400);
  }
});

test("the GCP service refuses an image request larger than any the edge sends", async () => {
  const response = await fetch(`${origin}/_ocel/image`, {
    method: "POST",
    body: JSON.stringify(payload({ url: `/${"a".repeat(20_000)}` })),
  });

  expect(response.status).toBe(413);
});

test("the GCP service hands every other request to Next", async () => {
  handedToNext.length = 0;

  for (const path of [
    "/docs/page",
    "/_next/image?url=%2Flogo.png&w=64&q=75",
    "/_ocel/image/extra",
  ]) {
    const response = await fetch(`${origin}${path}`);
    expect(await response.text(), path).toBe("next");
  }
  expect(handedToNext).toHaveLength(3);
});

test("the GCP service installs no image path where it holds no assets", () => {
  const next: Invoke = () => {};

  expect(newImageEndpointInvoke(next, {})).toBe(next);
});

test("the GCP service installs no image path unless it is told an edge posts images to it", () => {
  const next: Invoke = () => {};

  expect(newImageEndpointInvoke(next, { OCEL_ASSET_BUCKET: "ocel-acme-production" })).toBe(next);
});
