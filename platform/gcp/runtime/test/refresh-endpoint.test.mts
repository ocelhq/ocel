import http from "node:http";
import net from "node:net";
import type { CacheEntryFile } from "@framework/next-cache";
import { afterAll, afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import {
  newRefreshEndpoint,
  type RefreshEndpoint,
  readRefreshEndpoint,
} from "../src/next/refresh-endpoint.mjs";
import { refreshSignatureHeader, signRefreshTask } from "../src/next/refresh-signature.mjs";

const isrPrefix = "prod/shop/web/r1/isr";
const secret = "s-this-revision";

let origin: http.Server;
let originUrl: string;
let rendered: { url: string; headers: http.IncomingHttpHeaders }[];
let originStatus: number;
let originHangs: boolean;
let front: http.Server | undefined;
let warn: ReturnType<typeof vi.spyOn>;
let entries: Map<string, CacheEntryFile>;
let storesOnRender: boolean;
let readKeys: string[];
let readEntry: (key: string) => Promise<CacheEntryFile | null>;

beforeAll(async () => {
  origin = http.createServer((req, res) => {
    rendered.push({ url: String(req.url), headers: req.headers });
    if (originHangs) return;
    if (storesOnRender) entries.set("blog", { lastModified: Date.now(), value: {} });
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
  originHangs = false;
  entries = new Map();
  storesOnRender = true;
  readKeys = [];
  readEntry = async (key) => {
    readKeys.push(key);
    return entries.get(key) ?? null;
  };
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

function post(base: string, body: string, token = "good", signature?: string | null) {
  const signed = signature === undefined ? signRefreshTask(secret, Buffer.from(body)) : signature;
  return fetch(`${base}/_ocel/refresh`, {
    method: "POST",
    body,
    headers: {
      authorization: `Bearer ${token}`,
      ...(signed === null ? {} : { [refreshSignatureHeader]: signed }),
    },
  });
}

const endpointFor = (check = accepting, extra: { readBackTimeoutMs?: number } = {}) =>
  newRefreshEndpoint({
    path: "/_ocel/refresh",
    isrPrefix,
    secret,
    localOrigin: originUrl,
    check,
    readEntry: (key) => readEntry(key),
    ...extra,
  });

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

test("a refresh whose connection closes before its body is complete is answered as a bad request", async () => {
  const endpoint = endpointFor();
  const answered = new Promise<number>((resolve) => {
    front = http.createServer((req, res) => {
      endpoint(req, res).then(() => resolve(res.statusCode));
    });
  });
  await new Promise<void>((resolve) => front!.listen({ host: "127.0.0.1", port: 0 }, resolve));
  const port = (front!.address() as { port: number }).port;

  const socket = net.connect(port, "127.0.0.1");
  socket.write(
    "POST /_ocel/refresh HTTP/1.1\r\nhost: x\r\nauthorization: Bearer good\r\ncontent-length: 500\r\n\r\n{",
  );
  await new Promise((resolve) => setTimeout(resolve, 50));
  socket.destroy();

  expect(await answered).toBe(400);
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

test("a re-render that never answers fails after thirty seconds, so Cloud Tasks retries it", async () => {
  originHangs = true;
  const base = await serve(endpointFor());
  vi.useFakeTimers({ toFake: ["setTimeout"] });
  try {
    const pending = post(base, task());
    await vi.waitFor(() => expect(rendered).toHaveLength(1));

    await vi.advanceTimersByTimeAsync(29_000);
    expect(await Promise.race([pending, Promise.resolve("waiting")])).toBe("waiting");
    await vi.advanceTimersByTimeAsync(2_000);

    const response = await pending;
    expect(response.status).toBe(502);
    expect(await response.text()).toContain("did not answer within 30000ms");
  } finally {
    vi.useRealTimers();
  }
});

test("a refresh whose re-render stored a newer entry is done", async () => {
  const base = await serve(endpointFor());

  expect((await post(base, task())).status).toBe(204);
});

test("a refresh whose re-render answered but stored nothing newer fails, so Cloud Tasks retries it", async () => {
  storesOnRender = false;
  const base = await serve(endpointFor());

  const response = await post(base, task());

  expect(response.status).toBe(500);
  expect(await response.text()).toBe("the re-render of /blog?page=2 left no entry newer than 1000");
});

test("a refresh is not done while the store still holds only the entry it was asked to renew", async () => {
  storesOnRender = false;
  entries.set("blog", { lastModified: 1_000, value: {} });
  const base = await serve(endpointFor());

  expect((await post(base, task())).status).toBe(500);
});

test("a refresh whose entry cannot be read back fails, so Cloud Tasks retries it", async () => {
  readEntry = async () => {
    throw new Error("storage down");
  };
  const base = await serve(endpointFor());

  const response = await post(base, task());

  expect(response.status).toBe(503);
  expect(await response.text()).toBe("storage down");
});

test("a refresh whose read-back does not answer in time fails, so Cloud Tasks retries it", async () => {
  readEntry = () => new Promise(() => {});
  const base = await serve(endpointFor(accepting, { readBackTimeoutMs: 20 }));
  const started = Date.now();

  const response = await post(base, task());

  expect(response.status).toBe(503);
  expect(Date.now() - started).toBeLessThan(200);
});

test("a retried refresh finds the entry another attempt stored and is done", async () => {
  storesOnRender = false;
  entries.set("blog", { lastModified: 2_000, value: {} });
  const base = await serve(endpointFor());

  expect((await post(base, task())).status).toBe(204);
});

test("a refresh reads back the entry it names", async () => {
  const base = await serve(endpointFor());

  await post(base, task());

  expect(readKeys).toEqual(["blog"]);
});

test("a refresh whose re-render failed reads nothing back", async () => {
  originStatus = 500;
  const base = await serve(endpointFor());

  const response = await post(base, task());

  expect(response.status).toBe(502);
  expect(readKeys).toEqual([]);
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
  expect(readRefreshEndpoint({}, undefined, originUrl, async () => null)).toBeUndefined();
});

test("a service told a refresh url but no account refuses to start", () => {
  expect(() =>
    readRefreshEndpoint(
      { OCEL_REFRESH_URL: "https://web.run.app/_ocel/refresh", OCEL_ISR_PREFIX: isrPrefix },
      secret,
      originUrl,
      async () => null,
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
    secret,
    originUrl,
    async () => null,
  )!;
  const base = await serve(endpoint);

  expect((await fetch(`${base}/_ocel/refresh`)).status).toBe(405);
  expect((await fetch(`${base}/`)).status).toBe(418);
});

test("a service told a refresh url but no refresh secret refuses to start", () => {
  expect(() =>
    readRefreshEndpoint(
      {
        OCEL_REFRESH_URL: "https://web.run.app/_ocel/refresh",
        OCEL_REFRESH_ACCOUNT: "refresh@p.iam.gserviceaccount.com",
        OCEL_ISR_PREFIX: isrPrefix,
      },
      undefined,
      originUrl,
      async () => null,
    ),
  ).toThrow(/OCEL_REFRESH_SECRET/);
});

test("a refresh task carrying another app's signature is refused", async () => {
  const base = await serve(endpointFor());
  const body = task();

  const response = await post(
    base,
    body,
    "good",
    signRefreshTask("s-other-app", Buffer.from(body)),
  );

  expect(response.status).toBe(204);
  expect(rendered).toEqual([]);
  expect(readKeys).toEqual([]);
  expect(warn).toHaveBeenCalledWith(expect.stringMatching(/did not sign/));
});

test("a refresh task with no signature renders nothing", async () => {
  const base = await serve(endpointFor());

  const response = await post(base, task(), "good", null);

  expect(response.status).toBe(204);
  expect(rendered).toEqual([]);
  expect(warn).toHaveBeenCalledWith(expect.stringMatching(/did not sign/));
});

test("a refresh task changed after it was signed renders nothing", async () => {
  const base = await serve(endpointFor());
  const signature = signRefreshTask(secret, Buffer.from(task()));

  const response = await post(base, task({ url: "/admin" }), "good", signature);

  expect(response.status).toBe(204);
  expect(rendered).toEqual([]);
  expect(readKeys).toEqual([]);
});

test("a refresh's signature is checked before its body is read as a task", async () => {
  const base = await serve(endpointFor());

  const response = await post(base, "not json", "good", "0".repeat(64));

  expect(response.status).toBe(204);
  expect(warn).toHaveBeenCalledWith(expect.stringMatching(/did not sign/));
  expect(warn).not.toHaveBeenCalledWith(expect.stringMatching(/cannot read/));
});

test("a refresh without a valid token is refused before its signature is examined", async () => {
  const base = await serve(endpointFor());

  const response = await post(base, task(), "bad");

  expect(response.status).toBe(401);
  expect(warn).not.toHaveBeenCalled();
});
