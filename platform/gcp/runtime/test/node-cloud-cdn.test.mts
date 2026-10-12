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
