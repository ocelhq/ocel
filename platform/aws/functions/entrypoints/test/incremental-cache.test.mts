import { createHash } from "node:crypto";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterAll, beforeAll, expect, test } from "vitest";
import { loadIncrementalCacheFactory } from "../src/next/incremental-cache.mjs";
import { previewModeId, writeNextProjectFixture } from "./next-project-fixture.mjs";

let dir: string;

beforeAll(async () => {
  dir = await mkdtemp(join(tmpdir(), "ocel-inc-cache-"));
});
afterAll(async () => {
  await rm(dir, { recursive: true, force: true });
});

async function makeFixture(name: string, overrides: Record<string, unknown> = {}) {
  const projectDir = join(dir, name);
  await writeNextProjectFixture(projectDir, overrides);
  const make = await loadIncrementalCacheFactory(projectDir);
  const requireFromApp = createRequire(join(projectDir, "package.json"));
  const StubCacheHandler = requireFromApp(join(projectDir, "stub-cache-handler.cjs")).default;
  return { projectDir, make: make!, StubCacheHandler };
}

function fakeReq(headers: Record<string, string> = {}): any {
  return { headers };
}

test("generates fetch-cache keys in Next's format so both runtimes share a keyspace", async () => {
  const { make } = await makeFixture("keys");
  const cache: any = make(fakeReq());

  const expected = createHash("sha256")
    .update(JSON.stringify(["v4", "prefix", "my-invocation"]))
    .digest("hex");
  expect(await cache.generateSimpleCacheKey("my-invocation")).toBe(expected);
});

test("a set entry is served back fresh until its revalidate window passes", async () => {
  const { make } = await makeFixture("round-trip");
  const cache: any = make(fakeReq());

  const data = { headers: {}, body: JSON.stringify({ random: 42 }), status: 200, url: "" };
  await cache.set("key-1", { kind: "FETCH", data, revalidate: 60 }, { fetchCache: true });

  const entry = await cache.get("key-1", { kind: "FETCH", revalidate: 60 });
  expect(entry.isStale).toBe(false);
  expect(entry.value).toEqual({ kind: "FETCH", data, revalidate: 60 });
});

test("an entry older than its revalidate window comes back stale", async () => {
  const { make, StubCacheHandler } = await makeFixture("stale");
  const cache: any = make(fakeReq());

  const data = { headers: {}, body: "{}", status: 200, url: "" };
  StubCacheHandler.store.set("old-key", {
    lastModified: Date.now() - 120_000,
    value: { kind: "FETCH", data, revalidate: 60 },
  });

  const entry = await cache.get("old-key", { kind: "FETCH", revalidate: 60 });
  expect(entry.isStale).toBe(true);
});

test("only FETCH traffic passes through; other kinds read null and write nothing", async () => {
  const { make, StubCacheHandler } = await makeFixture("fetch-only");
  const cache: any = make(fakeReq());

  await cache.set("page-key", { kind: "PAGES", html: "<html/>" }, {});
  expect(StubCacheHandler.store.has("page-key")).toBe(false);
  expect(await cache.get("anything", { kind: "PAGES" })).toBeNull();

  StubCacheHandler.store.set("wrong-kind", {
    lastModified: Date.now(),
    value: { kind: "PAGES", html: "<html/>" },
  });
  expect(await cache.get("wrong-kind", { kind: "FETCH" })).toBeNull();
});

test("revalidateTag reaches the manifest-named handler", async () => {
  const { make, StubCacheHandler } = await makeFixture("revalidate-tag");
  const cache: any = make(fakeReq());

  await cache.revalidateTag(["t1"], { expire: 10 });
  expect(StubCacheHandler.revalidated).toContainEqual({ tags: ["t1"], durations: { expire: 10 } });
});

test("the on-demand revalidate header is honored only with the build's preview id", async () => {
  const { make } = await makeFixture("on-demand");

  const plain: any = make(fakeReq());
  expect(plain.isOnDemandRevalidate).toBe(false);

  const wrongId: any = make(fakeReq({ "x-prerender-revalidate": "not-it" }));
  expect(wrongId.isOnDemandRevalidate).toBe(false);

  const onDemand: any = make(fakeReq({ "x-prerender-revalidate": previewModeId }));
  expect(onDemand.isOnDemandRevalidate).toBe(true);
});

test("each request constructs its own handler with that request's headers", async () => {
  const { make, StubCacheHandler } = await makeFixture("per-request");

  make(fakeReq({ "x-probe": "one" }));
  make(fakeReq({ "x-probe": "two" }));

  const [first, second] = StubCacheHandler.instances.slice(-2);
  expect(first.opts._requestHeaders["x-probe"]).toBe("one");
  expect(second.opts._requestHeaders["x-probe"]).toBe("two");
  expect(first.opts.fetchCacheKeyPrefix).toBe("prefix");
  expect(first.opts.serverDistDir).toMatch(/\.next\/server$/);
});

test("returns null when the bundle has no required-server-files manifest", async () => {
  const projectDir = join(dir, "no-manifest");
  await writeNextProjectFixture(projectDir);
  await rm(join(projectDir, ".next/required-server-files.json"));

  expect(await loadIncrementalCacheFactory(projectDir)).toBeNull();
});

test("returns null when the manifest names no cacheHandler", async () => {
  const projectDir = join(dir, "no-handler");
  await writeNextProjectFixture(projectDir, { cacheHandler: undefined });

  expect(await loadIncrementalCacheFactory(projectDir)).toBeNull();
});

test("accepts a cache handler exported as plain module.exports", async () => {
  const projectDir = join(dir, "cjs-handler");
  await writeNextProjectFixture(projectDir);
  await writeFile(
    join(projectDir, "stub-cache-handler.cjs"),
    "class PlainHandler { async get() { return null; } }\nmodule.exports = PlainHandler;\n",
  );

  const make = await loadIncrementalCacheFactory(projectDir);
  const cache: any = make!(fakeReq());
  expect(await cache.get("k", { kind: "FETCH" })).toBeNull();
});
