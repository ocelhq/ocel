import http from "node:http";
import { afterAll, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { renderAtOrigin } from "../src/next/loopback-render.mjs";

let server: http.Server;
let origin: string;
let received: { url: string; headers: http.IncomingHttpHeaders }[];
let answer: { status: number; delayMs: number; truncate: boolean };

beforeAll(async () => {
  server = http.createServer((req, res) => {
    received.push({ url: String(req.url), headers: req.headers });
    setTimeout(() => {
      if (answer.truncate) {
        res.writeHead(answer.status, { "content-length": "100" });
        res.write("partial");
        res.socket?.destroy();
        return;
      }
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
  answer = { status: 200, delayMs: 0, truncate: false };
});

const blog = {
  url: "/blog?page=2",
  key: "blog",
  lastModified: 1_000,
  headers: { host: "shop.example" },
};

test("a re-render asks the origin for the page with the host it names and the generation it renews", async () => {
  await renderAtOrigin(origin, blog, 1_000);

  expect(received).toHaveLength(1);
  expect(received[0]!.url).toBe("/blog?page=2");
  expect(received[0]!.headers.host).toBe("shop.example");
  expect(received[0]!.headers["x-ocel-refresh"]).toBe("1000");
});

test("a re-render sends the origin no header the refresh carries beyond its host", async () => {
  await renderAtOrigin(
    origin,
    {
      ...blog,
      headers: {
        host: "shop.example",
        cookie: "session=1",
        authorization: "Bearer x",
        "x-ocel-entry": "/admin",
      },
    },
    1_000,
  );

  const { headers } = received[0]!;
  expect(headers.cookie).toBeUndefined();
  expect(headers.authorization).toBeUndefined();
  expect(headers["x-ocel-entry"]).toBeUndefined();
});

test("a re-render the origin answers with an error rejects naming its status", async () => {
  answer = { status: 500, delayMs: 0, truncate: false };

  await expect(renderAtOrigin(origin, blog, 1_000)).rejects.toThrow(/answered 500/);
});

test("a re-render whose origin outlasts its timeout rejects naming the timeout", async () => {
  answer = { status: 200, delayMs: 200, truncate: false };
  const started = Date.now();

  await expect(renderAtOrigin(origin, blog, 50)).rejects.toThrow(/did not answer within 50ms/);
  expect(Date.now() - started).toBeLessThan(150);
});

test("a re-render whose origin is not a url rejects and leaves no timer to fire", async () => {
  const uncaught = vi.fn();
  process.on("uncaughtException", uncaught);
  try {
    await expect(renderAtOrigin("not a url", blog, 50)).rejects.toThrow();
    await new Promise((resolve) => setTimeout(resolve, 100));
    expect(uncaught).not.toHaveBeenCalled();
  } finally {
    process.off("uncaughtException", uncaught);
  }
});

test("a re-render whose connection closes before its end rejects", async () => {
  answer = { status: 200, delayMs: 0, truncate: true };

  await expect(renderAtOrigin(origin, blog, 1_000)).rejects.toThrow();
});

test("a re-render of a url that leaves the origin sends nothing", async () => {
  for (const url of ["//evil/x", "/\\evil/x", "http://evil/"]) {
    await expect(renderAtOrigin(origin, { ...blog, url }, 1_000)).rejects.toThrow();
  }

  expect(received).toEqual([]);
});
