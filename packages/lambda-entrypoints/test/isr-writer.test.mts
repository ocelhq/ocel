import { afterEach, beforeEach, expect, test, vi } from "vitest";
import type { CacheEntryFile } from "@ocel/next-cache";

import { IsrWriteRejected, isrEntryWriter, writerAt } from "../src/next/isr-writer.mjs";

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

  await writerAt(WRITER_URL, "write-secret", impl).write("blog/post", entry);

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
    writerAt(WRITER_URL, "write-secret", impl).write("blog/post", entry),
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
    writerAt(WRITER_URL, "write-secret", impl).write("blog/post", entry),
  ).rejects.toBeInstanceOf(IsrWriteRejected);
  expect(logged).toHaveBeenCalledWith(expect.stringContaining("blog/post"));
});

test("a server-side rejection surfaces as an ordinary retryable failure", async () => {
  const { impl } = fakeFetch(new Response("Internal Error", { status: 503 }));

  const write = writerAt(WRITER_URL, "write-secret", impl).write("blog/post", entry);
  await expect(write).rejects.toThrow("status 503");
  await expect(write).rejects.not.toBeInstanceOf(IsrWriteRejected);
});

// A hung writer would otherwise hold the invocation open to the function's own
// timeout, billing for the wait — the PutObjectCommand this replaced carried the
// SDK's timeouts.
test("a write carries a timeout", async () => {
  const { impl, calls } = fakeFetch(new Response(null, { status: 204 }));

  await writerAt(WRITER_URL, "write-secret", impl).write("blog/post", entry);

  expect(calls[0][1].signal).toBeInstanceOf(AbortSignal);
});

// The writer is not optional: a deploy reading entries from an adopted cache
// store has no other way to write them, so a half-injected pair is a broken
// deploy and must say so rather than fall back to a standing credential.
test("a half-configured writer is a failure, not a fallback", () => {
  expect(() => isrEntryWriter()).toThrow(URL_ENV);

  process.env[URL_ENV] = WRITER_URL;
  expect(() => isrEntryWriter()).toThrow(SECRET_ENV);

  delete process.env[URL_ENV];
  process.env[SECRET_ENV] = "write-secret";
  expect(() => isrEntryWriter()).toThrow(URL_ENV);

  process.env[URL_ENV] = WRITER_URL;
  expect(isrEntryWriter()).not.toBeNull();
});
