import { beforeEach, describe, expect, test } from "vitest";
import { loadImageConfig, resetConfigMemo } from "../src/config.mjs";
import { SubstrateError } from "../src/errors.mjs";
import { configHash, fakeStore, imageConfig, serialize, storeWithConfig } from "./fixtures.mjs";

const ID = { slug: "proj1", app: "web", buildId: "build-1" };
const KEY = "image-config/proj1/web/build-1.json";

beforeEach(() => resetConfigMemo());

test("loads the config from the key the deploy wrote it to", async () => {
  const config = imageConfig();
  const store = storeWithConfig(config);
  await expect(loadImageConfig(store, ID, configHash(config))).resolves.toEqual(config);
  expect(store.reads).toEqual([KEY]);
});

test("the digest is over the stored bytes, not over a re-serialization", async () => {
  const config = imageConfig();
  const store = fakeStore();
  store.put(KEY, {
    bytes: new TextEncoder().encode(JSON.stringify(config)),
  });
  const reordered = serialize(config) !== JSON.stringify(config);
  expect(reordered).toBe(true);
  await expect(loadImageConfig(store, ID, configHash(config))).rejects.toThrow(SubstrateError);
});

test("refuses a config that does not hash to configHash", async () => {
  const config = imageConfig();
  const store = storeWithConfig(imageConfig({ domains: ["evil.example"] }));
  await expect(loadImageConfig(store, ID, configHash(config))).rejects.toThrow(
    /does not match configHash/,
  );
});

test("refuses a configHash that is not a sha256 digest", async () => {
  const store = storeWithConfig(imageConfig());
  await expect(loadImageConfig(store, ID, "../../etc/passwd")).rejects.toThrow(SubstrateError);
  expect(store.reads).toEqual([]);
});

test("refuses a missing config rather than serving without one", async () => {
  const store = fakeStore();
  await expect(loadImageConfig(store, ID, configHash(imageConfig()))).rejects.toThrow(
    /no image config at/,
  );
});

test("refuses bytes that hash correctly but are not JSON", async () => {
  const store = fakeStore();
  const bytes = new TextEncoder().encode("{not json");
  store.put(KEY, { bytes });
  const { createHash } = await import("node:crypto");
  const hash = createHash("sha256").update(bytes).digest("hex");
  await expect(loadImageConfig(store, ID, hash)).rejects.toThrow(/is not JSON/);
});

test("refuses an authentic config that is missing fields this side reads", async () => {
  const store = fakeStore();
  const partial = { path: "/_next/image", deviceSizes: [640] };
  const bytes = new TextEncoder().encode(JSON.stringify(partial));
  store.put(KEY, { bytes });
  const { createHash } = await import("node:crypto");
  const hash = createHash("sha256").update(bytes).digest("hex");
  await expect(loadImageConfig(store, ID, hash)).rejects.toThrow(/is missing/);
});

test("refuses an authentic config missing any single field this side reads", async () => {
  const { createHash } = await import("node:crypto");
  for (const field of [
    "path",
    "deviceSizes",
    "imageSizes",
    "formats",
    "domains",
    "remotePatterns",
    "minimumCacheTTL",
    "maximumResponseBody",
    "dangerouslyAllowSVG",
    "contentSecurityPolicy",
    "contentDispositionType",
  ] as const) {
    const store = fakeStore();
    const partial: Record<string, unknown> = { ...imageConfig() };
    delete partial[field];
    const bytes = new TextEncoder().encode(JSON.stringify(partial));
    store.put(KEY, { bytes });
    const hash = createHash("sha256").update(bytes).digest("hex");
    await expect(loadImageConfig(store, ID, hash)).rejects.toThrow(
      new RegExp(`is missing ${field}`),
    );
  }
});

describe("memoization", () => {
  test("a warm container reads the object once per configHash", async () => {
    const config = imageConfig();
    const store = storeWithConfig(config);
    const hash = configHash(config);
    await loadImageConfig(store, ID, hash);
    await loadImageConfig(store, ID, hash);
    await loadImageConfig(store, ID, hash);
    expect(store.reads).toEqual([KEY]);
  });

  test("a different configHash is a different entry", async () => {
    const first = imageConfig();
    const second = imageConfig({ minimumCacheTTL: 60 });
    const store = storeWithConfig(first);
    await loadImageConfig(store, ID, configHash(first));
    store.put(KEY, { bytes: new TextEncoder().encode(serialize(second)) });
    await expect(loadImageConfig(store, ID, configHash(second))).resolves.toEqual(second);
    expect(store.reads).toEqual([KEY, KEY]);
  });

  test("a rejected config is never memoized", async () => {
    const config = imageConfig();
    const store = storeWithConfig(imageConfig({ domains: ["evil.example"] }));
    const hash = configHash(config);
    await expect(loadImageConfig(store, ID, hash)).rejects.toThrow();
    store.put(KEY, { bytes: new TextEncoder().encode(serialize(config)) });
    await expect(loadImageConfig(store, ID, hash)).resolves.toEqual(config);
  });
});

test("caps the config read", async () => {
  const store = fakeStore();
  store.put(KEY, { bytes: new Uint8Array(2 * 1024 * 1024) });
  await expect(loadImageConfig(store, ID, configHash(imageConfig()))).rejects.toThrow(
    SubstrateError,
  );
});
