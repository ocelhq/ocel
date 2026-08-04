import { afterEach, beforeEach, expect, test, vi } from "vitest";
import type { CacheEntryFile } from "@ocel/next-cache";

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

// Two regenerators that raced both wrote a fresh render, so the loser's write is
// redundant rather than lost — and retrying it would turn a herd into a storm.
test("a rate-limited write is accepted without a retry", async () => {
  const { impl, calls } = fakeFetch(new Response("Too Many Requests", { status: 429 }));

  await expect(
    entryStoreAt(WRITER_URL, "write-secret", impl).write("blog/post", entry),
  ).resolves.toBeUndefined();
  expect(calls).toHaveLength(1);
});

// A 4xx is the writer saying "not this key, not ever" — the route re-renders on
// every request and caches on none. The caller runs under background(), which
// swallows what it catches, so a permanent rejection that arrived as a plain
// Error would be indistinguishable from the backpressure a 429 reports.
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

// A hung writer would otherwise hold the invocation open to the function's own
// timeout, billing for the wait — the PutObjectCommand this replaced carried the
// SDK's timeouts.
test("a write carries a timeout", async () => {
  const { impl, calls } = fakeFetch(new Response(null, { status: 204 }));

  await entryStoreAt(WRITER_URL, "write-secret", impl).write("blog/post", entry);

  expect(calls[0][1].signal).toBeInstanceOf(AbortSignal);
});

// The writer is not optional: a deploy reading entries from an adopted cache
// store has no other way to write them, so a half-injected pair is a broken
// deploy and must say so rather than fall back to a standing credential.
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

// A fetch impl that fails the way the network does, rather than answering.
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
  // The same key encoding the write uses, so both ops name one object.
  expect(url).toBe(`${WRITER_URL}?key=blog%2Fpost`);
  expect(init.method ?? "GET").toBe("GET");
  expect((init.headers as Record<string, string>).authorization).toBe("Bearer write-secret");
});

test("a read is bounded by a timeout of its own", async () => {
  const { impl, calls } = fakeFetch(new Response("Not Found", { status: 404 }));

  await entryStoreAt(WRITER_URL, "write-secret", impl).read("blog/post");

  expect(calls[0][1].signal).toBeInstanceOf(AbortSignal);
});

// THE property this path is written around. A read now sits on the serving path,
// where Next calls get() for every request to a cached route. An error raised
// here does not become a slow request, it becomes a broken one — so every
// failure degrades to a miss, which makes Next render. A writer outage has to
// cost latency and nothing else.
test.each([
  ["an absent entry", new Response("Not Found", { status: 404 })],
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

// A miss is the ordinary case and says nothing; every other failure is a cache
// that has stopped working, which is invisible in the response and shows up only
// as origin load.
test("a miss is silent and a failure is not", async () => {
  const warned = vi.spyOn(console, "warn").mockImplementation(() => {});

  const missing = fakeFetch(new Response("Not Found", { status: 404 })).impl;
  await entryStoreAt(WRITER_URL, "write-secret", missing).read("blog/post");
  expect(warned).not.toHaveBeenCalled();

  const broken = throwingFetch(new TypeError("fetch failed"));
  await entryStoreAt(WRITER_URL, "write-secret", broken).read("blog/post");
  expect(warned).toHaveBeenCalledWith(expect.stringContaining("blog/post"));
});
