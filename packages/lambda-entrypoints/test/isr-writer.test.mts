import { afterEach, beforeEach, expect, test, vi } from "vitest";
import type { CacheEntryFile } from "@ocel/next-cache";

import { isrEntryWriter, writerAt } from "../src/next/isr-writer.mjs";

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

test("any other rejection surfaces", async () => {
  const { impl } = fakeFetch(new Response("Unauthorized", { status: 401 }));

  await expect(
    writerAt(WRITER_URL, "write-secret", impl).write("blog/post", entry),
  ).rejects.toThrow("status 401");
});

test("no writer is configured unless both coordinates are injected", () => {
  expect(isrEntryWriter()).toBeNull();

  process.env[URL_ENV] = WRITER_URL;
  expect(isrEntryWriter()).toBeNull();

  delete process.env[URL_ENV];
  process.env[SECRET_ENV] = "write-secret";
  expect(isrEntryWriter()).toBeNull();

  process.env[URL_ENV] = WRITER_URL;
  expect(isrEntryWriter()).not.toBeNull();
});
