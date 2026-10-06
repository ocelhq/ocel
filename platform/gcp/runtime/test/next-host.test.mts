import http from "node:http";
import { installNextHost, refuseIncompleteHost } from "@framework/next-runtime/host";
import { expect, test, vi } from "vitest";
import { newGcpNextHost } from "../src/next/next-host.mjs";

test("the GCP host binds every network on the port Cloud Run names", () => {
  expect(newGcpNextHost({ PORT: "8080" }).bind).toEqual({ host: "0.0.0.0", port: 8080 });
});

test("the GCP host refuses to start where nothing names a port", () => {
  expect(() => newGcpNextHost({})).toThrow(/PORT/);
});

const MB = 1024 * 1024;

function fits(
  cache: {
    write(key: string, value: unknown, bytes: number): void;
    read<T>(key: string): T | undefined;
  },
  bytes: number,
): boolean {
  const key = `probe-${bytes}`;
  cache.write(key, "value", bytes);
  return cache.read(key) !== undefined;
}

test("the GCP host's in-instance cache holds one tenth of the memory its service runs with", () => {
  const cache = newGcpNextHost({ PORT: "8080", OCEL_FUNCTION_MEMORY_MB: "2048" }).instanceCache!;

  expect(fits(cache, Math.floor(2048 * MB * 0.1))).toBe(true);
  expect(fits(cache, Math.floor(2048 * MB * 0.1) + 1)).toBe(false);
});

test("the GCP host's in-instance cache holds 50 MB where its service names no memory", () => {
  const cache = newGcpNextHost({ PORT: "8080" }).instanceCache!;

  expect(fits(cache, 50 * MB)).toBe(true);
  expect(fits(cache, 50 * MB + 1)).toBe(false);
});

test("the GCP host installs every store an app with an incremental cache boots with", () => {
  const env = { PORT: "8080", OCEL_ISR_PREFIX: "prod/shop/web/r1/isr" };

  expect(refuseIncompleteHost(newGcpNextHost(env), env)).toBeUndefined();
});

test("the GCP host's cache store keeps an entry for the next request the instance serves", async () => {
  const host = newGcpNextHost({ PORT: "8080" });
  const store = await host.newCacheStore!();
  const entry = { lastModified: 1, value: { kind: "FETCH", data: {} } };

  await store.writeFetch("hash", entry);

  expect(await store.readFetch("hash")).toEqual(entry);
});

test("the GCP host keeps a fetch entry in the bucket and under the object prefix its service is told", async () => {
  const requests: { method?: string; url?: string }[] = [];
  const server = http.createServer((req, res) => {
    requests.push({ method: req.method, url: req.url });
    req.resume();
    res.setHeader("Content-Type", "application/json");
    res.end(JSON.stringify({ generation: "1" }));
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  try {
    const { port } = server.address() as { port: number };
    const env = {
      PORT: "8080",
      OCEL_ISR_PREFIX: "prod/shop/web/r1/isr",
      OCEL_ISR_BUCKET: "b",
      OCEL_ISR_OBJECT_PREFIX: "cache/shop/web/prod/r1/isr",
      OCEL_STORAGE_ENDPOINT: `http://127.0.0.1:${port}`,
      OCEL_TAG_DATABASE: "projects/p/databases/d",
    };
    const host = newGcpNextHost(env);
    const store = await host.newCacheStore!();

    await store.writeFetch("abc", { lastModified: 1, value: { kind: "FETCH", data: {} } });

    expect(requests).toHaveLength(1);
    const url = new URL(requests[0]!.url!, "http://x");
    expect(requests[0]!.method).toBe("POST");
    expect(url.pathname).toBe("/upload/storage/v1/b/b/o");
    expect(url.searchParams.get("name")).toBe(
      "cache/shop/web/prod/r1/isr/fetch-cache/abc.cache.json",
    );
    expect(refuseIncompleteHost(host, env)).toBeUndefined();
  } finally {
    server.close();
  }
});

async function listen(
  onRequest: (req: http.IncomingMessage, body: string) => void,
): Promise<{ server: http.Server; origin: string }> {
  const server = http.createServer((req, res) => {
    let body = "";
    req.on("data", (chunk) => {
      body += chunk;
    });
    req.on("end", () => {
      onRequest(req, body);
      res.setHeader("Content-Type", "application/json");
      res.end(
        JSON.stringify({
          commitTime: new Date().toISOString(),
          writeResults: [{ transformResults: [{ timestampValue: new Date().toISOString() }] }],
        }),
      );
    });
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  return { server, origin: `http://127.0.0.1:${(server.address() as { port: number }).port}` };
}

const tagEnv = {
  PORT: "8080",
  OCEL_ISR_PREFIX: "prod/shop/web/r1/isr",
  OCEL_ISR_BUCKET: "b",
  OCEL_ISR_OBJECT_PREFIX: "cache/shop/web/prod/r1/isr",
  OCEL_TAG_DATABASE: "projects/p/databases/d",
};

test("the GCP host records tags in the tags database its service is told", async () => {
  const requests: { method?: string; url?: string }[] = [];
  const { server, origin } = await listen((req) => {
    requests.push({ method: req.method, url: req.url });
  });
  try {
    const host = newGcpNextHost({ ...tagEnv, OCEL_FIRESTORE_ENDPOINT: origin });
    const store = await host.newCacheStore!();

    await store.writeTags(["cart"], { expired: 5 });

    expect(requests).toEqual([
      { method: "POST", url: "/v1/projects/p/databases/d/documents:commit" },
    ]);
  } finally {
    server.close();
  }
});

test("the GCP host's cache store and use-cache store record tags through one writer", async () => {
  const bodies: string[] = [];
  const { server, origin } = await listen((_req, body) => {
    bodies.push(body);
  });
  try {
    const host = newGcpNextHost({ ...tagEnv, OCEL_FIRESTORE_ENDPOINT: origin });
    const pages = await host.newCacheStore!();
    const useCache = await host.newUseCacheStore!();

    await Promise.all([
      pages.writeTags(["a"], { expired: 5 }),
      useCache.writeTag("b", { expired: 6, writtenAt: 1 }),
    ]);

    expect(bodies).toHaveLength(1);
    expect(JSON.parse(bodies[0]!).writes).toHaveLength(2);
  } finally {
    server.close();
  }
});

test("the GCP host's cache store and use-cache store share one tenth of the service's memory", async () => {
  const host = newGcpNextHost({ PORT: "8080", OCEL_FUNCTION_MEMORY_MB: "1" });
  const pages = await host.newCacheStore!();
  const useCache = await host.newUseCacheStore!();
  const html = "x".repeat(30_000);

  await pages.writeEntry("/a", {
    lastModified: 1,
    value: { kind: "PAGES", html, pageData: {}, status: 200, headers: {} },
  });
  await useCache.writeEntry("key", {
    tags: [],
    stale: 1,
    timestamp: 2,
    expire: 3,
    revalidate: 4,
    body: html,
  });

  expect(await pages.readEntry("/a")).toBeNull();
  expect(await useCache.readEntry("key")).toEqual({
    tags: [],
    stale: 1,
    timestamp: 2,
    expire: 3,
    revalidate: 4,
    body: html,
  });
});

test("the GCP host refreshes a stale page through the queue its deploy named", async () => {
  const requests: { method?: string; url?: string }[] = [];
  const { server, origin } = await listen((req) => {
    requests.push({ method: req.method, url: req.url });
  });
  try {
    const host = newGcpNextHost({
      PORT: "8080",
      OCEL_ISR_PREFIX: "prod/shop/web/r1/isr",
      OCEL_REFRESH_URL: "https://web-abc.a.run.app/_ocel/refresh",
      OCEL_REFRESH_QUEUE: "projects/p/locations/r/queues/q",
      OCEL_REFRESH_ACCOUNT: "ocel-production-refresh@p.iam.gserviceaccount.com",
      OCEL_REFRESH_SECRET: "s1",
      OCEL_TASKS_ENDPOINT: origin,
    });

    await host.scheduleRefresh!({ url: "/blog", key: "blog", lastModified: 1, headers: {} });

    expect(requests).toEqual([
      { method: "POST", url: "/v2/projects/p/locations/r/queues/q/tasks" },
    ]);
  } finally {
    server.close();
  }
});

test("a GCP host billed per request that routes its own requests refuses to start without a refresh queue", () => {
  expect(() =>
    newGcpNextHost({
      PORT: "8080",
      OCEL_ORIGIN_DISPATCH: "1",
      OCEL_FINISH_BEFORE_RESPONSE_MS: "10000",
      OCEL_ISR_PREFIX: "prod/shop/web/r1/isr",
    }),
  ).toThrow(/Cloud Tasks queue/);
});

test("a GCP host told a refresh url but no queue refuses to start", () => {
  expect(() =>
    newGcpNextHost({
      PORT: "8080",
      OCEL_ISR_PREFIX: "prod/shop/web/r1/isr",
      OCEL_REFRESH_URL: "https://web-abc.a.run.app/_ocel/refresh",
      OCEL_REFRESH_ACCOUNT: "ocel-production-refresh@p.iam.gserviceaccount.com",
    }),
  ).toThrow(/OCEL_REFRESH_QUEUE/);
});

test("a GCP host told a refresh url but no refresh secret refuses to start", () => {
  expect(() =>
    newGcpNextHost({
      PORT: "8080",
      OCEL_ISR_PREFIX: "prod/shop/web/r1/isr",
      OCEL_REFRESH_URL: "https://web-abc.a.run.app/_ocel/refresh",
      OCEL_REFRESH_QUEUE: "projects/p/locations/r/queues/q",
      OCEL_REFRESH_ACCOUNT: "ocel-production-refresh@p.iam.gserviceaccount.com",
    }),
  ).toThrow(/OCEL_REFRESH_SECRET/);
});

test("the GCP host takes its refresh secret out of the environment the app sees", () => {
  const env: NodeJS.ProcessEnv = {
    PORT: "8080",
    OCEL_ISR_PREFIX: "prod/shop/web/r1/isr",
    OCEL_REFRESH_URL: "https://web-abc.a.run.app/_ocel/refresh",
    OCEL_REFRESH_QUEUE: "projects/p/locations/r/queues/q",
    OCEL_REFRESH_ACCOUNT: "ocel-production-refresh@p.iam.gserviceaccount.com",
    OCEL_REFRESH_SECRET: "s1",
  };

  const host = newGcpNextHost(env);

  expect(env.OCEL_REFRESH_SECRET).toBeUndefined();
  expect(host.scheduleRefresh).toBeDefined();
});

test("a GCP host that does not dispatch at its origin schedules no refresh", () => {
  const host = newGcpNextHost({
    PORT: "8080",
    OCEL_FINISH_BEFORE_RESPONSE_MS: "10000",
    OCEL_ISR_PREFIX: "prod/shop/web/r1/isr",
  });

  expect(host.scheduleRefresh).toBeUndefined();
});

test("the GCP host's cache store and the cache the default use-cache handler fills share one tenth of the service's memory", async () => {
  vi.resetModules();
  const host = newGcpNextHost({ PORT: "8080", OCEL_FUNCTION_MEMORY_MB: "1" });
  installNextHost(host);
  const handler = (await import("@framework/next-runtime/use-cache-default")).default;
  const pages = await host.newCacheStore!();
  const html = "x".repeat(30_000);
  const bytes = new TextEncoder().encode("y".repeat(60_000));

  await pages.writeEntry("/a", {
    lastModified: 1,
    value: { kind: "PAGES", html, pageData: {}, status: 200, headers: {} },
  });
  await handler.set(
    "key",
    Promise.resolve({
      value: new ReadableStream({
        start(controller) {
          controller.enqueue(bytes);
          controller.close();
        },
      }),
      tags: [],
      stale: 1,
      timestamp: performance.timeOrigin + performance.now(),
      expire: 3600,
      revalidate: 3600,
    }),
  );

  expect(await handler.get("key", [])).toBeDefined();
  expect(await pages.readEntry("/a")).toBeNull();
});
