import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  configHash,
  imageConfig,
  serialize,
} from "@framework/next-image-optimizer/test-support/fixtures";
import { solid } from "@framework/next-image-optimizer/test-support/images";
import { refuseIncompleteHost } from "@framework/next-runtime/host";
import sharp from "sharp";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { newDiskObjectStore } from "../src/next/disk-assets.mjs";
import { newVpsNextHost } from "../src/next/next-host.mjs";

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

function hostEnv(): NodeJS.ProcessEnv {
  return { OCEL_BUILD_DIR: build };
}

test("a static file the build left in the image is read by the key the optimizer asks for", async () => {
  const store = newDiskObjectStore(build, storage);

  const object = await store.get("assets/shop/prod/web/r1a2b3c4d/_next/static/app.js", 1024);

  expect(new TextDecoder().decode(object?.bytes)).toBe("chunk");
});

test("a key outside the release's assets, or climbing out of the static files, reads nothing", async () => {
  const store = newDiskObjectStore(build, storage);

  for (const key of [
    "assets/shop/prod/web/r9z9z9z9z/_next/static/app.js",
    "assets/shop/prod/web/r1a2b3c4d/../../cache/index.cache.json",
    "assets/shop/prod/web/r1a2b3c4d/_next/static/missing.js",
    "assets/shop/prod/web/r1a2b3c4d/_next",
  ]) {
    expect(await store.get(key, 1024), key).toBeUndefined();
  }
});

test("the host keeps no cache store, so Next keeps its own cache in the container", () => {
  const env = hostEnv();
  const host = newVpsNextHost(env);

  expect(refuseIncompleteHost(host, env)).toBeUndefined();
  expect(host.newCacheStore).toBeUndefined();
  expect(host.newUseCacheStore).toBeUndefined();
});

test("a host with no build dir refuses to start, saying how to run it", () => {
  expect(() => newVpsNextHost({})).toThrow(
    "ocel: OCEL_BUILD_DIR is unset: run this image through ocel deploy",
  );
});

const purgeUrl = "https://shop.acct.workers.dev/.ocel/purge";

function frontedEnv(): NodeJS.ProcessEnv {
  return {
    ...hostEnv(),
    OCEL_PURGE_URL: purgeUrl,
    OCEL_PURGE_SECRET: "s3cret",
    OCEL_STORAGE_COORDINATE: "shop/prod/web/r1a2b3c4d",
  };
}

function capturePurges(): Request[] {
  const posted: Request[] = [];
  vi.stubGlobal("fetch", async (input: string | URL | Request, init?: RequestInit) => {
    posted.push(new Request(input, init));
    return new Response(null, { status: 204 });
  });
  return posted;
}

test("a tag revalidated on a box the worker fronts is purged at the edge under the box's storage coordinate", async () => {
  const posted = capturePurges();
  try {
    const { edgePurge } = newVpsNextHost(frontedEnv());
    await edgePurge!(new Map([["posts", { expired: Date.now() }]]));

    expect(posted.map(({ url }) => url)).toEqual([purgeUrl]);
    const body = (await posted[0]!.json()) as {
      storageCoordinate: string;
      tags: { tag: string }[];
    };
    expect(body.storageCoordinate).toBe("shop/prod/web/r1a2b3c4d");
    expect(body.tags.map(({ tag }) => tag)).toEqual(["posts"]);
  } finally {
    vi.unstubAllGlobals();
  }
});

test("on a box nothing fronts a revalidation is purged nowhere", () => {
  expect(newVpsNextHost(hostEnv()).edgePurge).toBeUndefined();
});

async function invokeOn(
  invoke: import("@framework/node-runtime/host").Invoke,
  request: Request,
): Promise<Response> {
  const http = await import("node:http");
  const server = http.createServer((req, res) => {
    void Promise.resolve(invoke(req, res, {} as never)).catch(() => res.destroy());
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  try {
    const { port } = server.address() as import("node:net").AddressInfo;
    const url = new URL(request.url);
    return await fetch(`http://127.0.0.1:${port}${url.pathname}${url.search}`, {
      method: request.method,
      headers: request.headers,
      body: request.method === "POST" ? await request.text() : undefined,
    });
  } finally {
    server.close();
  }
}

test("an image the worker in front of the box posts to the origin's image path is optimized from the build", async () => {
  const config = imageConfig();
  await writeFile(join(build, "static", "logo.png"), await solid("png", 400, 200));
  await writeFile(join(build, "image-config.json"), serialize(config));
  const env = {
    OCEL_BUILD_DIR: build,
    OCEL_STORAGE_COORDINATE: "shop/prod/web/r1a2b3c4d",
    OCEL_IMAGE_ENDPOINT: "1",
  };
  const handedToNext: string[] = [];
  const invoke = await newVpsNextHost(env).newOriginInvoke!((req, res) => {
    handedToNext.push(req.url ?? "");
    res.writeHead(200).end("next");
  });

  const response = await invokeOn(
    invoke,
    new Request("https://shop.example/_ocel/image", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({
        storage,
        url: "/logo.png",
        w: 64,
        q: 75,
        accept: "image/webp,*/*",
        mimeType: "image/webp",
        configHash: configHash(config),
      }),
    }),
  );

  expect(response.status).toBe(200);
  const resized = await sharp(Buffer.from(await response.arrayBuffer())).metadata();
  expect(resized).toMatchObject({ format: "webp", width: 64 });
  expect(handedToNext).toEqual([]);
});

test("a box no worker fronts hands its image path to Next like any other", async () => {
  const handedToNext: string[] = [];
  const invoke = await newVpsNextHost({
    ...hostEnv(),
    OCEL_STORAGE_COORDINATE: "shop/prod/web/r1a2b3c4d",
  }).newOriginInvoke!((req, res) => {
    handedToNext.push(req.url ?? "");
    res.writeHead(200).end("next");
  });

  await invokeOn(
    invoke,
    new Request("https://shop.example/_ocel/image", { method: "POST", body: "{}" }),
  );

  expect(handedToNext).toEqual(["/_ocel/image"]);
});
