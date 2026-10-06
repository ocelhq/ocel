import type { CacheEntryFile } from "@framework/next-cache";
import { entryMissHeader } from "@framework/next-cache";
import { afterEach, expect, test, vi } from "vitest";

import { IsrWriteRejected, newIsrWriterClient } from "../src/isr-writer.mjs";

const ENDPOINT = "https://writer.example";
const PREFIX = "prod/proj/app/BID";
const ENTRY_URL = `${ENDPOINT}/${PREFIX}/entry`;

const writerWith = (fetch: typeof globalThis.fetch) =>
  newIsrWriterClient({ endpoint: ENDPOINT, isrPrefix: PREFIX, secret: "write-secret", fetch });

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

  await writerWith(impl).writeEntry("blog/post", entry);

  expect(calls).toHaveLength(1);
  const [url, init] = calls[0]!;
  expect(url).toBe(`${ENTRY_URL}?key=blog%2Fpost`);
  expect(init.method).toBe("PUT");
  expect((init.headers as Record<string, string>).authorization).toBe("Bearer write-secret");
  expect(JSON.parse(init.body as string)).toEqual(entry);
});

test("a rate-limited write is accepted without a retry", async () => {
  const { impl, calls } = fakeFetch(new Response("Too Many Requests", { status: 429 }));

  await expect(writerWith(impl).writeEntry("blog/post", entry)).resolves.toBeUndefined();
  expect(calls).toHaveLength(1);
});

test("a permanent rejection is distinguishable from rate limiting, and is logged", async () => {
  const { impl } = fakeFetch(new Response("Bad Request", { status: 400 }));
  const logged = vi.spyOn(console, "error").mockImplementation(() => {});

  await expect(writerWith(impl).writeEntry("blog/post", entry)).rejects.toBeInstanceOf(
    IsrWriteRejected,
  );
  expect(logged).toHaveBeenCalledWith(expect.stringContaining("blog/post"));
});

test("a server-side rejection surfaces as an ordinary retryable failure", async () => {
  const { impl } = fakeFetch(new Response("Internal Error", { status: 503 }));

  const write = writerWith(impl).writeEntry("blog/post", entry);
  await expect(write).rejects.toThrow("status 503");
  await expect(write).rejects.not.toBeInstanceOf(IsrWriteRejected);
});

test("a write sets a timeout", async () => {
  const { impl, calls } = fakeFetch(new Response(null, { status: 204 }));

  await writerWith(impl).writeEntry("blog/post", entry);

  expect(calls[0]![1].signal).toBeInstanceOf(AbortSignal);
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
  const { impl, calls } = fakeFetch(new Response(JSON.stringify(entry), { status: 200 }));

  expect(await writerWith(impl).readEntry("blog/post")).toEqual(entry);

  expect(calls).toHaveLength(1);
  const [url, init] = calls[0]!;
  expect(url).toBe(`${ENTRY_URL}?key=blog%2Fpost`);
  expect(init.method ?? "GET").toBe("GET");
  expect((init.headers as Record<string, string>).authorization).toBe("Bearer write-secret");
});

test("a read is bounded by a timeout of its own", async () => {
  const { impl, calls } = fakeFetch(new Response("Not Found", { status: 404 }));

  await writerWith(impl).readEntry("blog/post");

  expect(calls[0]![1].signal).toBeInstanceOf(AbortSignal);
});

test.each([
  ["an absent entry", entryMissResponse()],
  ["a misdirected read", new Response("Not Found", { status: 404 })],
  ["an unreachable writer", new TypeError("fetch failed")],
  [
    "a timed-out read",
    Object.assign(new Error("The operation was aborted"), { name: "TimeoutError" }),
  ],
  ["a writer outage", new Response("Internal Error", { status: 503 })],
  ["a rejected credential", new Response("Unauthorized", { status: 401 })],
  ["a refused key", new Response("Bad Request", { status: 400 })],
  ["a rate-limited read", new Response("Too Many Requests", { status: 429 })],
  ["a truncated body", new Response("{not json", { status: 200 })],
])("fails open to a cache miss on %s", async (_name, outcome) => {
  vi.spyOn(console, "warn").mockImplementation(() => {});
  const impl = outcome instanceof Response ? fakeFetch(outcome).impl : throwingFetch(outcome);

  await expect(writerWith(impl).readEntry("blog/post")).resolves.toBeNull();
});

test("a miss is silent and a failure is not", async () => {
  const warned = vi.spyOn(console, "warn").mockImplementation(() => {});

  const missing = fakeFetch(entryMissResponse()).impl;
  await writerWith(missing).readEntry("blog/post");
  expect(warned).not.toHaveBeenCalled();

  const broken = throwingFetch(new TypeError("fetch failed"));
  await writerWith(broken).readEntry("blog/post");
  expect(warned).toHaveBeenCalledWith(expect.stringContaining("blog/post"));
});

test("a 404 from anywhere but the entry itself warns, and still misses", async () => {
  const warned = vi.spyOn(console, "warn").mockImplementation(() => {});
  const misdirected = fakeFetch(new Response("Not Found", { status: 404 })).impl;

  await expect(writerWith(misdirected).readEntry("blog/post")).resolves.toBeNull();
  expect(warned).toHaveBeenCalledWith(expect.stringContaining("blog/post"));
});

const snapshot = { version: 1, deployedAt: 1, generatedAt: 2, records: { a: { stale: 3 } } };

const TAGS_URL = `${ENDPOINT}/${PREFIX}/tags`;

test("reads a deployment's tag snapshot and the etag to ask with next", async () => {
  const { impl, calls } = fakeFetch(
    new Response(JSON.stringify(snapshot), { status: 200, headers: { etag: '"abc"' } }),
  );

  expect(await writerWith(impl).readTagSnapshot(null)).toEqual({
    kind: "fresh",
    snapshot,
    etag: '"abc"',
  });

  const [url, init] = calls[0]!;
  expect(url).toBe(TAGS_URL);
  const headers = init.headers as Record<string, string>;
  expect(headers.authorization).toBe("Bearer write-secret");
  expect(headers["if-none-match"]).toBeUndefined();
  expect(init.signal).toBeInstanceOf(AbortSignal);
});

test("answers unchanged when the writer says the snapshot has not moved", async () => {
  const { impl, calls } = fakeFetch(new Response(null, { status: 304 }));

  expect(await writerWith(impl).readTagSnapshot('"abc"')).toEqual({ kind: "unchanged" });

  expect((calls[0]![1].headers as Record<string, string>)["if-none-match"]).toBe('"abc"');
});

test("answers absent when the writer holds no snapshot for the deployment", async () => {
  const { impl } = fakeFetch(entryMissResponse());

  expect(await writerWith(impl).readTagSnapshot(null)).toEqual({ kind: "absent" });
});

test.each([
  ["a writer outage", () => new Response("Internal Error", { status: 500 })],
  ["a misdirected read", () => new Response("Not Found", { status: 404 })],
  ["a rejected credential", () => new Response("Unauthorized", { status: 401 })],
  ["an unreadable version", () => new Response(JSON.stringify({ version: 2 }), { status: 200 })],
])("fails a tag snapshot read the writer could not answer: %s", async (_name, respond) => {
  const { impl } = fakeFetch(respond());

  await expect(writerWith(impl).readTagSnapshot(null)).rejects.toThrow();
});

test("raises tags to the deployment's own path under its write secret", async () => {
  const { impl, calls } = fakeFetch(new Response(null, { status: 204 }));
  const records = { a: { stale: 3 }, b: { expired: 4 } };

  await writerWith(impl).raiseTags(records);

  const [url, init] = calls[0]!;
  expect(url).toBe(TAGS_URL);
  expect(init.method).toBe("POST");
  const headers = init.headers as Record<string, string>;
  expect(headers.authorization).toBe("Bearer write-secret");
  expect(headers["content-type"]).toBe("application/json");
  expect(JSON.parse(init.body as string)).toEqual({ records });
  expect(init.signal).toBeInstanceOf(AbortSignal);
});

test.each([429, 500])("surfaces a tag raise the writer refused: %i", async (status) => {
  const { impl } = fakeFetch(new Response("no", { status }));

  await expect(writerWith(impl).raiseTags({ a: { stale: 1 } })).rejects.toThrow(
    `raise ${PREFIX}: writer answered ${status}`,
  );
});

test("refuses to send the write secret to a writer that is not https", () => {
  expect(() =>
    newIsrWriterClient({ endpoint: "http://writer.example", isrPrefix: PREFIX, secret: "s" }),
  ).toThrow("not https");
});
