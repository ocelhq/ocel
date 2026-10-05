import { installNextHost, refuseIncompleteHost } from "@framework/next-runtime/host";
import { expect, test, vi } from "vitest";
import { newGcpNextHost } from "../src/next/next-host.mjs";

test("the GCP host binds every network on the port Cloud Run names", () => {
  expect(newGcpNextHost({ PORT: "8080" }).bind).toEqual({ host: "0.0.0.0", port: 8080 });
});

test("the GCP host refuses to start where nothing names a port", () => {
  expect(() => newGcpNextHost({})).toThrow(/PORT/);
});

const MB = 1024 * 1024;

function fits(
  cache: {
    write(key: string, value: unknown, bytes: number): void;
    read<T>(key: string): T | undefined;
  },
  bytes: number,
): boolean {
  const key = `probe-${bytes}`;
  cache.write(key, "value", bytes);
  return cache.read(key) !== undefined;
}

test("the GCP host's in-instance cache holds one tenth of the memory its service runs with", () => {
  const cache = newGcpNextHost({ PORT: "8080", OCEL_FUNCTION_MEMORY_MB: "2048" }).instanceCache!;

  expect(fits(cache, Math.floor(2048 * MB * 0.1))).toBe(true);
  expect(fits(cache, Math.floor(2048 * MB * 0.1) + 1)).toBe(false);
});

test("the GCP host's in-instance cache holds 50 MB where its service names no memory", () => {
  const cache = newGcpNextHost({ PORT: "8080" }).instanceCache!;

  expect(fits(cache, 50 * MB)).toBe(true);
  expect(fits(cache, 50 * MB + 1)).toBe(false);
});

test("the GCP host installs every store an app with an incremental cache boots with", () => {
  const env = { PORT: "8080", OCEL_ISR_PREFIX: "prod/shop/web/r1/isr" };

  expect(refuseIncompleteHost(newGcpNextHost(env), env)).toBeUndefined();
});

test("the GCP host's cache store keeps an entry for the next request the instance serves", async () => {
  const host = newGcpNextHost({ PORT: "8080" });
  const store = await host.newCacheStore!();
  const entry = { lastModified: 1, value: { kind: "FETCH", data: {} } };

  await store.writeFetch("hash", entry);

  expect(await store.readFetch("hash")).toEqual(entry);
});

test("the GCP host's cache store and use-cache store share one tenth of the service's memory", async () => {
  const host = newGcpNextHost({ PORT: "8080", OCEL_FUNCTION_MEMORY_MB: "1" });
  const pages = await host.newCacheStore!();
  const useCache = await host.newUseCacheStore!();
  const html = "x".repeat(30_000);

  await pages.writeEntry("/a", {
    lastModified: 1,
    value: { kind: "PAGES", html, pageData: {}, status: 200, headers: {} },
  });
  await useCache.writeEntry("key", {
    tags: [],
    stale: 1,
    timestamp: 2,
    expire: 3,
    revalidate: 4,
    body: html,
  });

  expect(await pages.readEntry("/a")).toBeNull();
});

test("the GCP host's cache store and the cache the default use-cache handler fills share one tenth of the service's memory", async () => {
  vi.resetModules();
  const host = newGcpNextHost({ PORT: "8080", OCEL_FUNCTION_MEMORY_MB: "1" });
  installNextHost(host);
  const handler = (await import("@framework/next-runtime/use-cache-default")).default;
  const pages = await host.newCacheStore!();
  const html = "x".repeat(30_000);
  const bytes = new TextEncoder().encode("y".repeat(60_000));

  await pages.writeEntry("/a", {
    lastModified: 1,
    value: { kind: "PAGES", html, pageData: {}, status: 200, headers: {} },
  });
  await handler.set(
    "key",
    Promise.resolve({
      value: new ReadableStream({
        start(controller) {
          controller.enqueue(bytes);
          controller.close();
        },
      }),
      tags: [],
      stale: 1,
      timestamp: performance.timeOrigin + performance.now(),
      expire: 3600,
      revalidate: 3600,
    }),
  );

  expect(await handler.get("key", [])).toBeDefined();
  expect(await pages.readEntry("/a")).toBeNull();
});
