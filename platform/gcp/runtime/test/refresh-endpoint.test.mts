import http from "node:http";
import { afterAll, afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import {
  newRefreshEndpoint,
  type RefreshEndpoint,
  readRefreshEndpoint,
} from "../src/next/refresh-endpoint.mjs";

const isrPrefix = "prod/shop/web/r1/isr";

let origin: http.Server;
let originUrl: string;
let rendered: { url: string; headers: http.IncomingHttpHeaders }[];
let originStatus: number;
let front: http.Server | undefined;
let warn: ReturnType<typeof vi.spyOn>;

beforeAll(async () => {
  origin = http.createServer((req, res) => {
    rendered.push({ url: String(req.url), headers: req.headers });
    res.writeHead(originStatus);
    res.end("rendered");
  });
  await new Promise<void>((resolve) => origin.listen({ host: "127.0.0.1", port: 0 }, resolve));
  originUrl = `http://127.0.0.1:${(origin.address() as { port: number }).port}`;
});

afterAll(() => new Promise<void>((resolve) => origin.close(() => resolve())));

beforeEach(() => {
  rendered = [];
  originStatus = 200;
  warn = vi.spyOn(console, "warn").mockImplementation(() => {});
});

afterEach(async () => {
  warn.mockRestore();
  const closing = front;
  front = undefined;
  if (closing) await new Promise<void>((resolve) => closing.close(() => resolve()));
});

async function serve(endpoint: RefreshEndpoint): Promise<string> {
  const server = http.createServer((req, res) => {
    endpoint(req, res).then((handled) => {
      if (!handled) {
        res.statusCode = 418;
        res.end();
      }
    });
  });
  front = server;
  await new Promise<void>((resolve) => server.listen({ host: "127.0.0.1", port: 0 }, resolve));
  return `http://127.0.0.1:${(server.address() as { port: number }).port}`;
}

const accepting = async (authorization: string | undefined) => authorization === "Bearer good";

const task = (refresh: Record<string, unknown> = {}, override: Record<string, unknown> = {}) =>
  JSON.stringify({
    isrPrefix,
    refresh: {
      url: "/blog?page=2",
      key: "blog",
      lastModified: 1_000,
      headers: { host: "shop.example" },
      ...refresh,
    },
    ...override,
  });

function post(base: string, body: string, token = "good") {
  return fetch(`${base}/_ocel/refresh`, {
    method: "POST",
    body,
    headers: { authorization: `Bearer ${token}` },
  });
}

const endpointFor = (check = accepting) =>
  newRefreshEndpoint({ path: "/_ocel/refresh", isrPrefix, localOrigin: originUrl, check });

test("a verified refresh re-renders its page at the local origin with the generation it renews", async () => {
  const base = await serve(endpointFor());

  const response = await post(base, task());

  expect(response.status).toBe(204);
  expect(rendered).toHaveLength(1);
  expect(rendered[0]!.url).toBe("/blog?page=2");
  expect(rendered[0]!.headers["x-ocel-refresh"]).toBe("1000");
  expect(rendered[0]!.headers.host).toBe("shop.example");
});

test("a refresh re-renders with the host it names and no other header the task carries", async () => {
  const base = await serve(endpointFor());

  await post(base, task({ headers: { host: "shop.example", cookie: "session=1" } }));

  expect(rendered[0]!.headers.cookie).toBeUndefined();
});

test("a refresh without a valid token is refused and renders nothing", async () => {
  const base = await serve(endpointFor());

  const missing = await fetch(`${base}/_ocel/refresh`, { method: "POST", body: task() });
  const bad = await post(base, task(), "bad");

  expect(missing.status).toBe(401);
  expect(bad.status).toBe(401);
  expect(rendered).toEqual([]);
});

test("Google's keys being unreadable answers so Cloud Tasks retries", async () => {
  const base = await serve(
    endpointFor(async () => {
      throw new Error("no keys");
    }),
  );

  const response = await post(base, task());

  expect(response.status).toBe(503);
  expect(rendered).toEqual([]);
});

test("a refresh for another deployment is dropped without a render", async () => {
  const base = await serve(endpointFor());

  const response = await post(base, task({}, { isrPrefix: "prod/shop/web/r0/isr" }));

  expect(response.status).toBe(204);
  expect(rendered).toEqual([]);
  expect(warn).toHaveBeenCalled();
});

test("a refresh task this runtime cannot read is dropped, never retried", async () => {
  const base = await serve(endpointFor());

  const response = await post(base, "not json");

  expect(response.status).toBe(204);
  expect(rendered).toEqual([]);
  expect(warn).toHaveBeenCalled();
});

test("a refresh body larger than a task can be is refused unread", async () => {
  const base = await serve(endpointFor());

  const response = await post(base, "x".repeat(70_000));

  expect(response.status).toBe(413);
  expect(rendered).toEqual([]);
});

test("a refresh whose re-render fails answers so Cloud Tasks retries it", async () => {
  originStatus = 500;
  const base = await serve(endpointFor());

  const response = await post(base, task());

  expect(response.status).toBe(502);
  expect(await response.text()).not.toContain("good");
});

test("a request for the refresh path other than a POST is refused and never reaches the app", async () => {
  const base = await serve(endpointFor());

  const response = await fetch(`${base}/_ocel/refresh`);

  expect(response.status).toBe(405);
  expect(response.headers.get("allow")).toBe("POST");
});

test("a request to any other path is left to the app", async () => {
  const base = await serve(endpointFor());

  const response = await fetch(`${base}/blog`);

  expect(response.status).toBe(418);
});

test("a service told no refresh url has no refresh endpoint", () => {
  expect(readRefreshEndpoint({}, originUrl)).toBeUndefined();
});

test("a service told a refresh url but no account refuses to start", () => {
  expect(() =>
    readRefreshEndpoint(
      { OCEL_REFRESH_URL: "https://web.run.app/_ocel/refresh", OCEL_ISR_PREFIX: isrPrefix },
      originUrl,
    ),
  ).toThrow(/OCEL_REFRESH_ACCOUNT/);
});

test("the refresh endpoint answers at the path of the url the service is told", async () => {
  const endpoint = readRefreshEndpoint(
    {
      OCEL_REFRESH_URL: "https://web.run.app/_ocel/refresh",
      OCEL_REFRESH_ACCOUNT: "refresh@p.iam.gserviceaccount.com",
      OCEL_ISR_PREFIX: isrPrefix,
    },
    originUrl,
  )!;
  const base = await serve(endpoint);

  expect((await fetch(`${base}/_ocel/refresh`)).status).toBe(405);
  expect((await fetch(`${base}/`)).status).toBe(418);
});
