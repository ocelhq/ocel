import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { entryMissHeader } from "@framework/next-cache";
import type { CacheEntryFile } from "@framework/next-cache";

import { IsrWriteRejected, entryStoreAt, isrEntryStore } from "../src/next/isr-writer.mjs";

const URL_ENV = "OCEL_ISR_WRITER_URL";
const SECRET_ENV = "OCEL_ISR_WRITER_SECRET";
const WRITER_URL = "https://writer.example/prod/proj/app/BID/entry";

beforeEach(() => {
  delete process.env[URL_ENV];
  delete process.env[SECRET_ENV];
});

afterEach(() => {
  vi.restoreAllMocks();
});

const entry = { lastModified: 1, value: { kind: "APP_PAGE" } } as unknown as CacheEntryFile;

function fakeFetch(response: Response) {
  const calls: Array<[string, RequestInit]> = [];
  const impl = (async (url: any, init: any) => {
    calls.push([String(url), init]);
    return response;
  }) as unknown as typeof fetch;
  return { impl, calls };
}

test("a write PUTs the entry under its cache key with the deploy's secret", async () => {
  const { impl, calls } = fakeFetch(new Response(null, { status: 204 }));

  await entryStoreAt(WRITER_URL, "write-secret", impl).write("blog/post", entry);

  expect(calls).toHaveLength(1);
  const [url, init] = calls[0];
  expect(url).toBe(`${WRITER_URL}?key=blog%2Fpost`);
  expect(init.method).toBe("PUT");
  expect((init.headers as Record<string, string>).authorization).toBe("Bearer write-secret");
  expect(JSON.parse(init.body as string)).toEqual(entry);
});

test("a rate-limited write is accepted without a retry", async () => {
  const { impl, calls } = fakeFetch(new Response("Too Many Requests", { status: 429 }));

  await expect(
    entryStoreAt(WRITER_URL, "write-secret", impl).write("blog/post", entry),
  ).resolves.toBeUndefined();
  expect(calls).toHaveLength(1);
});

test("a permanent rejection is distinguishable from rate limiting, and is logged", async () => {
  const { impl } = fakeFetch(new Response("Bad Request", { status: 400 }));
  const logged = vi.spyOn(console, "error").mockImplementation(() => {});

  await expect(
    entryStoreAt(WRITER_URL, "write-secret", impl).write("blog/post", entry),
  ).rejects.toBeInstanceOf(IsrWriteRejected);
  expect(logged).toHaveBeenCalledWith(expect.stringContaining("blog/post"));
});

test("a server-side rejection surfaces as an ordinary retryable failure", async () => {
  const { impl } = fakeFetch(new Response("Internal Error", { status: 503 }));

  const write = entryStoreAt(WRITER_URL, "write-secret", impl).write("blog/post", entry);
  await expect(write).rejects.toThrow("status 503");
  await expect(write).rejects.not.toBeInstanceOf(IsrWriteRejected);
});

test("a write carries a timeout", async () => {
  const { impl, calls } = fakeFetch(new Response(null, { status: 204 }));

  await entryStoreAt(WRITER_URL, "write-secret", impl).write("blog/post", entry);

  expect(calls[0][1].signal).toBeInstanceOf(AbortSignal);
});

test("a half-configured writer is a failure, not a fallback", () => {
  expect(() => isrEntryStore()).toThrow(URL_ENV);

  process.env[URL_ENV] = WRITER_URL;
  expect(() => isrEntryStore()).toThrow(SECRET_ENV);

  delete process.env[URL_ENV];
  process.env[SECRET_ENV] = "write-secret";
  expect(() => isrEntryStore()).toThrow(URL_ENV);

  process.env[URL_ENV] = WRITER_URL;
  expect(isrEntryStore()).not.toBeNull();
});

function entryMissResponse() {
  return new Response("Not Found", { status: 404, headers: { [entryMissHeader]: "1" } });
}

function throwingFetch(err: unknown) {
  return (async () => {
    throw err;
  }) as unknown as typeof fetch;
}

test("a read GETs the entry at its cache key with the deploy's secret", async () => {
  const { impl, calls } = fakeFetch(
    new Response(JSON.stringify(entry), { status: 200 }),
  );

  expect(await entryStoreAt(WRITER_URL, "write-secret", impl).read("blog/post")).toEqual(entry);

  expect(calls).toHaveLength(1);
  const [url, init] = calls[0];
  expect(url).toBe(`${WRITER_URL}?key=blog%2Fpost`);
  expect(init.method ?? "GET").toBe("GET");
  expect((init.headers as Record<string, string>).authorization).toBe("Bearer write-secret");
});

test("a read is bounded by a timeout of its own", async () => {
  const { impl, calls } = fakeFetch(new Response("Not Found", { status: 404 }));

  await entryStoreAt(WRITER_URL, "write-secret", impl).read("blog/post");

  expect(calls[0][1].signal).toBeInstanceOf(AbortSignal);
});

test.each([
  ["an absent entry", entryMissResponse()],
  ["a misdirected read", new Response("Not Found", { status: 404 })],
  ["an unreachable writer", new TypeError("fetch failed")],
  ["a timed-out read", Object.assign(new Error("The operation was aborted"), { name: "TimeoutError" })],
  ["a writer outage", new Response("Internal Error", { status: 503 })],
  ["a rejected credential", new Response("Unauthorized", { status: 401 })],
  ["a refused key", new Response("Bad Request", { status: 400 })],
  ["a rate-limited read", new Response("Too Many Requests", { status: 429 })],
  ["a truncated body", new Response("{not json", { status: 200 })],
])("fails open to a cache miss on %s", async (_name, outcome) => {
  vi.spyOn(console, "warn").mockImplementation(() => {});
  const impl =
    outcome instanceof Response ? fakeFetch(outcome).impl : throwingFetch(outcome);

  await expect(
    entryStoreAt(WRITER_URL, "write-secret", impl).read("blog/post"),
  ).resolves.toBeNull();
});

test("a miss is silent and a failure is not", async () => {
  const warned = vi.spyOn(console, "warn").mockImplementation(() => {});

  const missing = fakeFetch(entryMissResponse()).impl;
  await entryStoreAt(WRITER_URL, "write-secret", missing).read("blog/post");
  expect(warned).not.toHaveBeenCalled();

  const broken = throwingFetch(new TypeError("fetch failed"));
  await entryStoreAt(WRITER_URL, "write-secret", broken).read("blog/post");
  expect(warned).toHaveBeenCalledWith(expect.stringContaining("blog/post"));
});

test("a 404 from anywhere but the entry itself warns, and still misses", async () => {
  const warned = vi.spyOn(console, "warn").mockImplementation(() => {});
  const misdirected = fakeFetch(new Response("Not Found", { status: 404 })).impl;

  await expect(
    entryStoreAt(WRITER_URL, "write-secret", misdirected).read("blog/post"),
  ).resolves.toBeNull();
  expect(warned).toHaveBeenCalledWith(expect.stringContaining("blog/post"));
});
