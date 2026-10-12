import http from "node:http";
import type { AddressInfo } from "node:net";
import { afterEach, expect, test, vi } from "vitest";
import { withholdOverTaggedResponsesFromCloudCdn } from "../src/node/cloud-cdn.mjs";

afterEach(() => {
  vi.restoreAllMocks();
});

async function serveTagged(tags: string): Promise<Response> {
  class TestResponse extends http.ServerResponse {}
  withholdOverTaggedResponsesFromCloudCdn(TestResponse.prototype);
  const server = http.createServer({ ServerResponse: TestResponse }, (req, res) => {
    res.setHeader("cache-control", "s-maxage=60, stale-while-revalidate=540");
    res.setHeader("cache-tag", tags);
    res.end("page");
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  try {
    return await fetch(`http://127.0.0.1:${(server.address() as AddressInfo).port}/blog/a`);
  } finally {
    await new Promise((resolve) => server.close(resolve));
  }
}

test("a node response with a tag Cloud CDN's purge cannot name is not stored", async () => {
  const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
  const long = `r1a2b3c4d|path:/${"a".repeat(120)}`;

  const res = await serveTagged(`r1a2b3c4d|posts,${long}`);

  expect(res.headers.get("cache-control")).toBe(
    "private, no-cache, no-store, max-age=0, must-revalidate",
  );
  expect(res.headers.has("cache-tag")).toBe(false);
  expect(warn).toHaveBeenCalledWith(expect.stringContaining(long));
});

test("a node response with more tags than Cloud CDN keeps is not stored", async () => {
  vi.spyOn(console, "warn").mockImplementation(() => {});
  const tags = Array.from({ length: 51 }, (_, i) => `r1a2b3c4d|t${i}`).join(",");

  const res = await serveTagged(tags);

  expect(res.headers.get("cache-control")).toContain("no-store");
  expect(res.headers.has("cache-tag")).toBe(false);
});

test("a node response whose tags fit Cloud CDN's limits keeps its lifetime and tags", async () => {
  const res = await serveTagged("r1a2b3c4d|posts,r1a2b3c4d|path:/blog/a");

  expect(res.headers.get("cache-control")).toBe("s-maxage=60, stale-while-revalidate=540");
  expect(res.headers.get("cache-tag")).toBe("r1a2b3c4d|posts,r1a2b3c4d|path:/blog/a");
});

async function seenFor(
  respond: (res: http.ServerResponse) => void,
): Promise<{ cacheControl: string | null; tags: string | null; other: string | null }> {
  class TestResponse extends http.ServerResponse {}
  withholdOverTaggedResponsesFromCloudCdn(TestResponse.prototype);
  const server = http.createServer({ ServerResponse: TestResponse }, (_req, res) => {
    respond(res);
    res.end("page");
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  try {
    const res = await fetch(`http://127.0.0.1:${(server.address() as AddressInfo).port}/blog/a`);
    return {
      cacheControl: res.headers.get("cache-control"),
      tags: res.headers.get("cache-tag"),
      other: res.headers.get("x-other"),
    };
  } finally {
    await new Promise((resolve) => server.close(resolve));
  }
}

test("a node response whose over-limit tags are passed to writeHead is not stored", async () => {
  vi.spyOn(console, "warn").mockImplementation(() => {});
  const tags = Array.from({ length: 51 }, (_, i) => `r1a2b3c4d|t${i}`).join(",");
  const expected = {
    cacheControl: "private, no-cache, no-store, max-age=0, must-revalidate",
    tags: null,
    other: "kept",
  };

  const asObject = await seenFor((res) =>
    res.writeHead(200, { "cache-control": "s-maxage=60", "Cache-Tag": tags, "x-other": "kept" }),
  );
  const asArray = await seenFor((res) =>
    res.writeHead(200, "Fine", [
      "cache-control",
      "s-maxage=60",
      "cache-tag",
      tags,
      "x-other",
      "kept",
    ]),
  );

  expect(asObject).toEqual(expected);
  expect(asArray).toEqual(expected);
});

test("a node response whose tags passed to writeHead fit keeps its lifetime and tags", async () => {
  const seen = await seenFor((res) =>
    res.writeHead(200, { "cache-control": "s-maxage=60", "cache-tag": "r1a2b3c4d|posts" }),
  );

  expect(seen.cacheControl).toBe("s-maxage=60");
  expect(seen.tags).toBe("r1a2b3c4d|posts");
});
