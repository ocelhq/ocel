import { refuseIncompleteHost } from "@framework/next-runtime/host";
import { expect, test } from "vitest";
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
