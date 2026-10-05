import { chmod, mkdir, mkdtemp, rm, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { afterAll, beforeAll, expect, test } from "vitest";
import { newDiskAssetBucket } from "../src/next/disk-assets.mjs";

const assetPrefix = "prod/shop/web/r1/assets";

let dir: string;

beforeAll(async () => {
  dir = await mkdtemp(join(tmpdir(), "ocel-gcp-assets-"));
  for (const [rel, body] of Object.entries({
    "assets/_next/static/app.js": "chunk",
    "assets/404.html": "<p>missing</p>",
    "image-config.json": "{}",
  })) {
    await mkdir(dirname(join(dir, rel)), { recursive: true });
    await writeFile(join(dir, rel), body);
  }
  await writeFile(join(tmpdir(), "ocel-outside.txt"), "secret");
});

afterAll(async () => {
  await rm(dir, { recursive: true, force: true });
  await rm(join(tmpdir(), "ocel-outside.txt"), { force: true });
});

async function read(key: string): Promise<string | null> {
  const object = await newDiskAssetBucket(dir, assetPrefix).get(key);
  return object?.body ? new Response(object.body).text() : null;
}

test("an asset is read from the directory the image holds the app's assets in", async () => {
  expect(await read(`${assetPrefix}/_next/static/app.js`)).toBe("chunk");
  expect(await read(`${assetPrefix}/404.html`)).toBe("<p>missing</p>");
});

test("the image config is read beside the assets, as it is stored beside them", async () => {
  expect(await read("prod/shop/web/r1/image-config.json")).toBe("{}");
});

test("an asset the image does not hold is missing", async () => {
  expect(await read(`${assetPrefix}/_next/static/gone.js`)).toBeNull();
  expect(await read(`${assetPrefix}/_next`)).toBeNull();
});

test("a key outside the release the service serves is missing", async () => {
  expect(await read("prod/shop/web/r0/assets/_next/static/app.js")).toBeNull();
  expect(await read(`${assetPrefix}/../../../../../../ocel-outside.txt`)).toBeNull();
  expect(await read(`${assetPrefix}/%2e%2e/%2e%2e/ocel-outside.txt`)).toBeNull();
});

test("an asset's etag names its content", async () => {
  const bucket = newDiskAssetBucket(dir, assetPrefix);
  const [chunk, again, page] = await Promise.all([
    bucket.get(`${assetPrefix}/_next/static/app.js`),
    bucket.get(`${assetPrefix}/_next/static/app.js`),
    bucket.get(`${assetPrefix}/404.html`),
  ]);

  expect(chunk?.httpEtag).toMatch(/^".+"$/);
  expect(again?.httpEtag).toBe(chunk?.httpEtag);
  expect(page?.httpEtag).not.toBe(chunk?.httpEtag);
});

test("a disk asset whose read fails once is read again on the next request", async () => {
  const file = join(dir, "assets", "flaky.js");
  await writeFile(file, "flaky");
  await chmod(file, 0o000);
  const bucket = newDiskAssetBucket(dir, assetPrefix);

  await expect(bucket.get(`${assetPrefix}/flaky.js`)).rejects.toThrow();

  await chmod(file, 0o644);
  const object = await bucket.get(`${assetPrefix}/flaky.js`);
  expect(object?.httpEtag).toMatch(/^".+"$/);
  expect(await new Response(object?.body).text()).toBe("flaky");
});

test("an asset that links to a file outside the directory is missing", async () => {
  await symlink(join(tmpdir(), "ocel-outside.txt"), join(dir, "assets", "escape.txt"));

  expect(await read(`${assetPrefix}/escape.txt`)).toBeNull();
});

test("an asset under a directory that links outside the directory is missing", async () => {
  await symlink(tmpdir(), join(dir, "assets", "escape-dir"));

  expect(await read(`${assetPrefix}/escape-dir/ocel-outside.txt`)).toBeNull();
});

test("an asset that links to a file inside the directory is read", async () => {
  await symlink(join(dir, "assets", "404.html"), join(dir, "assets", "alias.html"));

  expect(await read(`${assetPrefix}/alias.html`)).toBe("<p>missing</p>");
});

test("an asset is read where the directory itself is reached through a link", async () => {
  const alias = `${dir}-alias`;
  await symlink(dir, alias);
  try {
    const object = await newDiskAssetBucket(alias, assetPrefix).get(
      `${assetPrefix}/_next/static/app.js`,
    );
    expect(await new Response(object?.body).text()).toBe("chunk");
  } finally {
    await rm(alias, { force: true });
  }
});
