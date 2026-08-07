import http from "node:http";
import { afterAll, beforeAll, expect, test } from "vitest";
import { fetchToNodeHandler } from "../src/node/fetch-bridge.mjs";

// A fetch-handler app echoing the URL it was handed — the value every absolute
// URL such an app builds (a Location, a canonical tag) is derived from.
const invoke = fetchToNodeHandler(
  (request) =>
    new Response(JSON.stringify({ url: request.url }), {
      headers: { "content-type": "application/json" },
    }),
);

let server: http.Server;
let port: number;

beforeAll(async () => {
  server = http.createServer((req, res) => void invoke(req, res, { waitUntil: () => {} }));
  await new Promise<void>((resolve) => server.listen({ host: "127.0.0.1", port: 0 }, resolve));
  port = (server.address() as any).port;
});

afterAll(async () => {
  await new Promise<void>((resolve) => server.close(() => resolve()));
});

// node's fetch refuses to send a Host header, and the Host is half of what is
// under test, so requests go out over node:http where both headers are ours.
function get(path: string, headers: Record<string, string>): Promise<any> {
  return new Promise((resolve, reject) => {
    const req = http.request({ host: "127.0.0.1", port, path, headers }, (res) => {
      let body = "";
      res.on("data", (d) => (body += d));
      res.on("end", () => resolve(JSON.parse(body)));
    });
    req.on("error", reject);
    req.end();
  });
}

test("request.url carries the public origin, not the loopback one", async () => {
  const body = await get("/x?a=1", {
    host: "app.ocel.site",
    "x-forwarded-proto": "https",
  });

  expect(body.url).toBe("https://app.ocel.site/x?a=1");
});

test("a comma-joined x-forwarded-proto uses its leftmost scheme", async () => {
  const body = await get("/x", {
    host: "app.ocel.site",
    "x-forwarded-proto": "https, http",
  });

  expect(body.url).toBe("https://app.ocel.site/x");
});

test("no x-forwarded-proto falls back to http", async () => {
  const body = await get("/x", { host: "app.ocel.site" });

  expect(body.url).toBe("http://app.ocel.site/x");
});
