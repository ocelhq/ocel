import http from "node:http";
import type { AddressInfo } from "node:net";
import { gzipSync } from "node:zlib";
import { afterAll, beforeAll, expect, test } from "vitest";
import { fetchUndecoded } from "../src/undecoded-fetch.mjs";

const gzipped = gzipSync(Buffer.from("hello from the origin"));

let server: http.Server;
let origin: string;

beforeAll(async () => {
  server = http.createServer(async (req, res) => {
    const chunks: Buffer[] = [];
    for await (const chunk of req) chunks.push(chunk as Buffer);
    if (req.url === "/gzip") {
      res.writeHead(200, {
        "content-encoding": "gzip",
        "content-length": String(gzipped.length),
        "content-type": "text/plain",
      });
      res.end(gzipped);
      return;
    }
    if (req.url === "/redirect") {
      res.writeHead(307, { location: "/elsewhere" });
      res.end();
      return;
    }
    if (req.url === "/cookies") {
      res.setHeader("set-cookie", ["a=1", "b=2"]);
      res.end();
      return;
    }
    res.writeHead(200, { "content-type": "application/json" });
    res.end(
      JSON.stringify({
        method: req.method,
        url: req.url,
        header: req.headers["x-probe"],
        host: req.headers.host,
        body: Buffer.concat(chunks).toString(),
      }),
    );
  });
  await new Promise<void>((resolve) => server.listen({ host: "127.0.0.1", port: 0 }, resolve));
  origin = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
});

afterAll(async () => {
  await new Promise<void>((resolve) => server.close(() => resolve()));
});

test("a gzip body the origin encoded itself arrives with its own bytes and headers", async () => {
  const response = await fetchUndecoded(`${origin}/gzip`, {
    headers: { "accept-encoding": "gzip" },
  });

  expect(response.headers.get("content-encoding")).toBe("gzip");
  expect(response.headers.get("content-length")).toBe(String(gzipped.length));
  expect(Buffer.from(await response.arrayBuffer())).toEqual(gzipped);
});

test("a request carries its method, headers and body to the origin", async () => {
  const response = await fetchUndecoded(
    new Request(`${origin}/echo?q=1`, {
      method: "POST",
      headers: { "x-probe": "yes" },
      body: "payload",
    }),
  );

  expect(await response.json()).toEqual({
    method: "POST",
    url: "/echo?q=1",
    header: "yes",
    host: new URL(origin).host,
    body: "payload",
  });
});

test("a host the caller names gives way to the url's, as fetch's does", async () => {
  const response = await fetchUndecoded(`${origin}/echo`, {
    headers: { host: "client.example" },
  });

  expect(((await response.json()) as { host: string }).host).toBe(new URL(origin).host);
});

test("a streamed request body reaches the origin", async () => {
  const body = new ReadableStream({
    start(controller) {
      controller.enqueue(new TextEncoder().encode("str"));
      controller.enqueue(new TextEncoder().encode("eamed"));
      controller.close();
    },
  });
  const response = await fetchUndecoded(`${origin}/echo`, {
    method: "PUT",
    body,
    duplex: "half",
  } as RequestInit);

  expect(((await response.json()) as { body: string }).body).toBe("streamed");
});

test("a redirect comes back to the caller rather than being followed", async () => {
  const response = await fetchUndecoded(`${origin}/redirect`);

  expect(response.status).toBe(307);
  expect(response.headers.get("location")).toBe("/elsewhere");
});

test("every set-cookie the origin sends stays a separate cookie", async () => {
  const response = await fetchUndecoded(`${origin}/cookies`);

  expect(response.headers.getSetCookie()).toEqual(["a=1", "b=2"]);
});

test("an aborted request rejects with the signal's reason", async () => {
  const controller = new AbortController();
  controller.abort(new Error("gone"));

  await expect(fetchUndecoded(`${origin}/echo`, { signal: controller.signal })).rejects.toThrow(
    "gone",
  );
});
