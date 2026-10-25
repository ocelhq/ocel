import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  configHash,
  imageConfig,
  serialize,
} from "@framework/next-image-optimizer/test-support/fixtures";
import { solid } from "@framework/next-image-optimizer/test-support/images";
import { dispatchRequest } from "@framework/next-runtime/dispatch-host";
import { refuseIncompleteHost } from "@framework/next-runtime/host";
import sharp from "sharp";
import { afterEach, beforeEach, expect, test } from "vitest";
import { newDiskAssetBucket } from "../src/next/disk-assets.mjs";
import { newVpsNextHost, readVpsDispatchHost } from "../src/next/next-host.mjs";

const storage = { project: "shop", env: "prod", app: "web", release: "r1a2b3c4d" };

let build: string;

beforeEach(async () => {
  build = await mkdtemp(join(tmpdir(), "ocel-vps-build-"));
  await mkdir(join(build, "static", "_next", "static"), { recursive: true });
  await writeFile(join(build, "static", "_next", "static", "app.js"), "chunk");
  await mkdir(join(build, "cache"), { recursive: true });
  await writeFile(
    join(build, "cache", "index.cache.json"),
    JSON.stringify({ lastModified: 1, value: { kind: "APP_PAGE", html: "<p>seed</p>" } }),
  );
});

afterEach(async () => {
  await rm(build, { recursive: true, force: true });
});

async function text(body: ReadableStream | null | undefined): Promise<string> {
  return body ? new Response(body).text() : "";
}

function hostEnv(): NodeJS.ProcessEnv {
  return { OCEL_BUILD_DIR: build, OCEL_ORIGIN_DISPATCH: "1" };
}

test("a static file the build left in the image is read by the key the dispatcher asks for", async () => {
  const bucket = newDiskAssetBucket(build, storage);

  const object = await bucket.get("assets/shop/prod/web/r1a2b3c4d/_next/static/app.js");

  expect(await text(object?.body)).toBe("chunk");
});

test("a key outside the release's assets, or climbing out of the static files, reads nothing", async () => {
  const bucket = newDiskAssetBucket(build, storage);

  for (const key of [
    "assets/shop/prod/web/r9z9z9z9z/_next/static/app.js",
    "assets/shop/prod/web/r1a2b3c4d/../../cache/index.cache.json",
    "assets/shop/prod/web/r1a2b3c4d/_next/static/missing.js",
    "assets/shop/prod/web/r1a2b3c4d/_next",
  ]) {
    expect(await bucket.get(key), key).toBeNull();
  }
});

test("the host keeps no cache store, so Next keeps its own cache in the container", () => {
  const env = { OCEL_BUILD_DIR: build, OCEL_ORIGIN_DISPATCH: "1" };
  const host = newVpsNextHost(env);

  expect(refuseIncompleteHost(host, env)).toBeUndefined();
  expect(host.newCacheStore).toBeUndefined();
  expect(host.newUseCacheStore).toBeUndefined();
});

test("an image the build left in the image is optimized by the container that serves it", async () => {
  const config = imageConfig();
  await writeFile(join(build, "static", "logo.png"), await solid("png", 400, 200));
  await writeFile(join(build, "image-config.json"), serialize(config));
  await writeFile(
    join(build, "routing.json"),
    JSON.stringify({
      entry: "server",
      rootFunction: "server",
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
  const env = {
    ...hostEnv(),
    OCEL_NEXT_ROUTE_TABLE: join(build, "routing.json"),
    OCEL_STORAGE_COORDINATE: "shop/prod/web/r1a2b3c4d",
  };

  const response = await dispatchRequest(
    new Request("https://shop.example/_next/image?url=%2Flogo.png&w=64&q=75", {
      headers: { accept: "image/webp,*/*" },
    }),
    readVpsDispatchHost(env, "http://127.0.0.1:1"),
    () => {},
  );

  expect(response.status).toBe(200);
  const resized = await sharp(Buffer.from(await response.arrayBuffer())).metadata();
  expect(resized).toMatchObject({ format: "webp", width: 64 });
});

test("a host with no build dir refuses to start, saying how to run it", () => {
  expect(() => newVpsNextHost({})).toThrow(
    "ocel: OCEL_BUILD_DIR is unset: run this image through ocel deploy",
  );
});
