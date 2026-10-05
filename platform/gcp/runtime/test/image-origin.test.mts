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
import sharp from "sharp";
import { afterAll, beforeAll, expect, test } from "vitest";
import { readGcpDispatchHost } from "../src/next/dispatch-host.mjs";

const assetPrefix = "prod/shop/web/r1/assets";

let dir: string;
let env: NodeJS.ProcessEnv;

beforeAll(async () => {
  dir = await mkdtemp(join(tmpdir(), "ocel-gcp-image-"));
  const config = imageConfig();
  const staticDir = join(dir, "static");
  await mkdir(join(staticDir, "assets"), { recursive: true });
  await writeFile(join(staticDir, "assets", "logo.png"), await solid("png", 400, 200));
  await writeFile(join(staticDir, "image-config.json"), serialize(config));
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
    OCEL_ROUTING_MANIFEST: join(dir, "routing.json"),
    OCEL_STATIC_DIR: staticDir,
    OCEL_ASSET_PREFIX: assetPrefix,
  };
});

afterAll(async () => {
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
