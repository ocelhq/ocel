import http from "node:http";
import { afterAll, beforeAll, beforeEach, expect, test } from "vitest";
import { newInstanceRefresh } from "../src/next/instance-refresh.mjs";

let server: http.Server;
let origin: string;
let received: { url: string; refresh: string; host: string }[];
let answer: { status: number; delayMs: number };

beforeAll(async () => {
  server = http.createServer((req, res) => {
    received.push({
      url: String(req.url),
      refresh: String(req.headers["x-ocel-refresh"]),
      host: String(req.headers.host),
    });
    setTimeout(() => {
      res.writeHead(answer.status);
      res.end("rendered");
    }, answer.delayMs);
  });
  await new Promise<void>((resolve) => server.listen({ host: "127.0.0.1", port: 0 }, resolve));
  const { port } = server.address() as { port: number };
  origin = `http://127.0.0.1:${port}`;
});

afterAll(() => new Promise<void>((resolve) => server.close(() => resolve())));

beforeEach(() => {
  received = [];
  answer = { status: 200, delayMs: 0 };
});

const blog = {
  url: "/blog?page=2",
  key: "blog",
  lastModified: 1_000,
  headers: { host: "shop.example", "x-ocel-refresh": "1000" },
};

test("a refresh re-renders the page on the instance that served it stale", async () => {
  await newInstanceRefresh(() => origin, 1_000)(blog);

  expect(received).toEqual([{ url: "/blog?page=2", refresh: "1000", host: "shop.example" }]);
});

test("a refresh settles only once the re-render has answered", async () => {
  answer = { status: 200, delayMs: 50 };
  const started = Date.now();

  await newInstanceRefresh(() => origin, 1_000)(blog);

  expect(Date.now() - started).toBeGreaterThanOrEqual(45);
});

test("refreshes of one entry asked for at once re-render it once", async () => {
  answer = { status: 200, delayMs: 20 };
  const refresh = newInstanceRefresh(() => origin, 1_000);

  await Promise.all([refresh(blog), refresh(blog), refresh({ ...blog, url: "/about" })]);

  expect(received.map((seen) => seen.url).sort()).toEqual(["/about", "/blog?page=2"]);
});

test("a refresh the re-render fails rejects, so the stale entry is refreshed by the next hit", async () => {
  answer = { status: 500, delayMs: 0 };
  const refresh = newInstanceRefresh(() => origin, 1_000);

  await expect(refresh(blog)).rejects.toThrow(/500/);
  answer = { status: 200, delayMs: 0 };
  await refresh(blog);
  expect(received).toHaveLength(2);
});

test("a refresh whose origin cannot be read rejects instead of throwing", async () => {
  const refresh = newInstanceRefresh(() => {
    throw new Error("no server");
  }, 1_000);
  let rejected: Promise<void> | undefined;

  expect(() => {
    rejected = refresh(blog);
  }).not.toThrow();
  await expect(rejected).rejects.toThrow(/no server/);
});
