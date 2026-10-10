import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  configHash,
  imageConfig,
  serialize,
} from "@framework/next-image-optimizer/test-support/fixtures";
import { solid } from "@framework/next-image-optimizer/test-support/images";
import { dispatchRequest } from "@framework/next-runtime/dispatch-host";
import sharp from "sharp";
import { afterAll, beforeAll, expect, test } from "vitest";
import { readGcpDispatchHost } from "../src/next/dispatch-host.mjs";
import { newCloudStorageBucket } from "./cloud-storage-bucket.mjs";
import { type ServedBucket, serveBucket } from "./serve-bucket.mjs";

const assetPrefix = "prod/shop/web/r1/assets";

const bucket = newCloudStorageBucket();
let stored: ServedBucket;
let dir: string;
let env: NodeJS.ProcessEnv;

beforeAll(async () => {
  dir = await mkdtemp(join(tmpdir(), "ocel-gcp-image-"));
  const config = imageConfig();
  bucket.objects.set(`assets/${assetPrefix}/logo.png`, {
    body: await solid("png", 400, 200),
    generation: "1",
  });
  bucket.objects.set("assets/prod/shop/web/r1/image-config.json", {
    body: serialize(config),
    generation: "1",
  });
  stored = await serveBucket(bucket);
  await writeFile(
    join(dir, "routing.json"),
    JSON.stringify({
      entry: "bundle-0",
      buildId: "b1",
      basePath: "",
      pathnames: [],
      routes: {
        beforeMiddleware: [],
        beforeFiles: [],
        afterFiles: [],
        dynamicRoutes: [],
        onMatch: [],
        fallback: [],
      },
      dispatch: {},
      images: { ...config, configHash: configHash(config) },
    }),
  );
  env = {
    OCEL_NEXT_ROUTE_TABLE: join(dir, "routing.json"),
    OCEL_ASSET_BUCKET: "ocel-acme-production",
    OCEL_STORAGE_ENDPOINT: stored.endpoint,
    OCEL_ASSET_PREFIX: assetPrefix,
  };
});

afterAll(async () => {
  await stored.close();
  await rm(dir, { recursive: true, force: true });
});

function image(query: string): Promise<Response> {
  return dispatchRequest(
    new Request(`https://shop.example/_next/image?${query}`, {
      headers: { accept: "image/webp,*/*" },
    }),
    readGcpDispatchHost(env, "http://127.0.0.1:1"),
    () => {},
  );
}

test("an image the app serves is resized by the service that serves it", async () => {
  const response = await image("url=%2Flogo.png&w=64&q=75");

  expect(response.status).toBe(200);
  expect(response.headers.get("content-type")).toBe("image/webp");
  const resized = await sharp(Buffer.from(await response.arrayBuffer())).metadata();
  expect(resized).toMatchObject({ format: "webp", width: 64 });
});

test("an image the app does not hold is refused as Next refuses it", async () => {
  const response = await image("url=%2Fmissing.png&w=64&q=75");

  expect(response.status).toBe(400);
});
