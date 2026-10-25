import http from "node:http";
import { installNextHost, refuseIncompleteHost } from "@framework/next-runtime/host";
import { expect, test, vi } from "vitest";
import { newGcpNextHost } from "../src/next/next-host.mjs";

test("the GCP host shapes at most forty-nine tags, leaving room for the release tag", () => {
  expect(newGcpNextHost({ PORT: "8080" }).cacheTagsPerObject).toBe(49);
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
  const env = { PORT: "8080", OCEL_STORAGE_COORDINATE: "shop/prod/web/r1a2b3c4d" };

  expect(refuseIncompleteHost(newGcpNextHost(env), env)).toBeUndefined();
});

test("the GCP host's cache store keeps an entry for the next request the instance serves", async () => {
  const host = newGcpNextHost({ PORT: "8080" });
  const store = await host.newCacheStore!();
  const entry = { lastModified: 1, value: { kind: "FETCH", data: {} } };

  await store.writeFetch("hash", entry);

  expect(await store.readFetch("hash")).toEqual(entry);
});

test("the GCP host keeps a fetch entry in the cache bucket and under the release its service is told", async () => {
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
      OCEL_STORAGE_COORDINATE: "shop/prod/web/r1a2b3c4d",
      OCEL_EDGE_BUCKET: "e",
      OCEL_CACHE_BUCKET: "c",
      OCEL_STORAGE_ENDPOINT: `http://127.0.0.1:${port}`,
      OCEL_TAG_DATABASE: "projects/p/databases/d",
    };
    const host = newGcpNextHost(env);
    const store = await host.newCacheStore!();

    await store.writeFetch("abc", { lastModified: 1, value: { kind: "FETCH", data: {} } });

    expect(requests).toHaveLength(1);
    const url = new URL(requests[0]!.url!, "http://x");
    expect(requests[0]!.method).toBe("POST");
    expect(url.pathname).toBe("/upload/storage/v1/b/c/o");
    expect(url.searchParams.get("name")).toBe("fetch-cache/shop/prod/web/r1a2b3c4d/abc.cache.json");
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
  OCEL_STORAGE_COORDINATE: "shop/prod/web/r1a2b3c4d",
  OCEL_EDGE_BUCKET: "e",
  OCEL_CACHE_BUCKET: "c",
  OCEL_TAG_DATABASE: "projects/p/databases/d",
};

test("the GCP host records tags in the tags database its service is told", async () => {
  const requests: { method?: string; url?: string }[] = [];
  const { server, origin } = await listen((req) => {
    requests.push({ method: req.method, url: req.url });
  });
  try {
    const host = newGcpNextHost({ ...tagEnv, OCEL_FIRESTORE_ENDPOINT: origin });
    const store = await host.newUseCacheStore!();

    await store.writeTag("cart", { expired: 5, writtenAt: 1 });

    expect(requests).toEqual([
      { method: "POST", url: "/v1/projects/p/databases/d/documents:commit" },
    ]);
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

const purgeEnv = {
  ...tagEnv,
  OCEL_CACHE_TAG_PURGE: "1",
  OCEL_RELEASE_TOKEN: "r1a2b3c4d",
};

async function withCloudCdnStub(
  run: (seen: { kind: "commit" | "purge"; body: string }[], origin: string) => Promise<void>,
): Promise<void> {
  const seen: { kind: "commit" | "purge"; body: string }[] = [];
  const { server, origin } = await listen((req, body) => {
    if (req.url?.endsWith(":commit")) seen.push({ kind: "commit", body });
  });
  const realFetch = globalThis.fetch;
  vi.stubGlobal("fetch", async (input: string | URL | Request, init?: RequestInit) => {
    const url = String(input);
    if (url.includes("metadata.google.internal")) {
      return Response.json({ access_token: "t", expires_in: 3600 });
    }
    if (url.startsWith("https://compute.googleapis.com/")) {
      seen.push({ kind: "purge", body: String(init?.body) });
      return Response.json({});
    }
    return realFetch(input, init);
  });
  try {
    await run(seen, origin);
  } finally {
    vi.unstubAllGlobals();
    server.close();
  }
}

test("a GCP host told a url map clears each tag it records, after the record lands", async () => {
  await withCloudCdnStub(async (seen, origin) => {
    const host = newGcpNextHost({
      ...purgeEnv,
      OCEL_CDN_URL_MAP: "projects/p/global/urlMaps/m",
      OCEL_FIRESTORE_ENDPOINT: origin,
    });
    const useCache = await host.newUseCacheStore!();

    await useCache.writeTag("cart", { expired: 5, writtenAt: 1 });
    await useCache.writeTag("shelf", { expired: 6, writtenAt: 1 });

    expect(seen.map((s) => s.kind)).toEqual(["commit", "purge", "commit", "purge"]);
    expect(JSON.parse(seen[1]!.body)).toEqual({ cacheTags: ["r1a2b3c4d|cart"] });
    expect(JSON.parse(seen[3]!.body)).toEqual({ cacheTags: ["r1a2b3c4d|shelf"] });
  });
});

test("a GCP host told no url map clears nothing", async () => {
  await withCloudCdnStub(async (seen, origin) => {
    const host = newGcpNextHost({ ...purgeEnv, OCEL_FIRESTORE_ENDPOINT: origin });
    const useCache = await host.newUseCacheStore!();

    await useCache.writeTag("cart", { expired: 5, writtenAt: 1 });

    expect(seen.map((s) => s.kind)).toEqual(["commit"]);
  });
});

test("a GCP host told a url map but no release refuses to start", () => {
  expect(() =>
    newGcpNextHost({
      ...tagEnv,
      OCEL_STORAGE_COORDINATE: "",
      OCEL_CDN_URL_MAP: "projects/p/global/urlMaps/m",
    }),
  ).toThrow(/OCEL_CDN_URL_MAP/);
});

test("the GCP host told where the worker purges clears each tag it records at the worker, signed with the secret its service is told", async () => {
  const fetched: { url: URL; init: RequestInit | undefined }[] = [];
  const { server, origin } = await listen(() => {});
  const realFetch = globalThis.fetch;
  vi.stubGlobal("fetch", async (input: string | URL | Request, init?: RequestInit) => {
    const url = new URL(typeof input === "string" || input instanceof URL ? input : input.url);
    if (url.hostname !== "shop.acct.workers.dev") return realFetch(input, init);
    fetched.push({ url, init });
    return new Response(null, { status: 202 });
  });
  try {
    const host = newGcpNextHost({
      ...purgeEnv,
      OCEL_FIRESTORE_ENDPOINT: origin,
      OCEL_PURGE_SECRET: "s",
      OCEL_PURGE_URL: "https://shop.acct.workers.dev/.ocel/purge",
    });
    const store = await host.newUseCacheStore!();

    await Promise.all([
      store.writeTag("a", { expired: 5, writtenAt: 1 }),
      store.writeTag("b", { expired: 5, writtenAt: 1 }),
    ]);

    const bodies = fetched.map((call) => JSON.parse(String(call.init?.body)));
    expect(
      bodies.every(
        (body) => body.storageCoordinate === "shop/prod/web/r1a2b3c4d" && body.mode === "purge",
      ),
    ).toBe(true);
    expect(
      bodies.flatMap((body) => body.tags.map((tag: { tag: string }) => tag.tag)).sort(),
    ).toEqual(["a", "b"]);
  } finally {
    vi.unstubAllGlobals();
    server.close();
  }
});

test("the GCP host told where the worker purges and no release refuses to start", () => {
  expect(() =>
    newGcpNextHost({
      ...tagEnv,
      OCEL_PURGE_SECRET: "s",
      OCEL_PURGE_URL: "https://shop.acct.workers.dev/.ocel/purge",
    }),
  ).toThrow(/OCEL_PURGE_URL/);
});

test("the GCP host told where the worker purges and no secret to sign with refuses to start", () => {
  expect(() =>
    newGcpNextHost({
      ...purgeEnv,
      OCEL_PURGE_URL: "https://shop.acct.workers.dev/.ocel/purge",
    }),
  ).toThrow(/OCEL_PURGE_SECRET/);
});

test("the GCP host keeps pages in Cloud Storage and tags in the tags database, whoever fronts it", async () => {
  const calls: URL[] = [];
  const { server, origin } = await listen((req) => {
    calls.push(new URL(req.url!, "http://x"));
  });
  try {
    const host = newGcpNextHost({
      ...tagEnv,
      OCEL_FIRESTORE_ENDPOINT: origin,
      OCEL_STORAGE_ENDPOINT: origin,
    });
    const store = await host.newCacheStore!();

    await store.writeEntry("blog", { lastModified: 1, value: { kind: "FETCH", data: {} } });
    const useCache = await host.newUseCacheStore!();
    await useCache.writeTag("a", { expired: 5, writtenAt: 1 });

    expect(calls.map((url) => url.pathname)).toEqual([
      "/upload/storage/v1/b/e/o",
      "/v1/projects/p/databases/d/documents:commit",
    ]);
    expect(calls[0]!.searchParams.get("name")).toBe(
      "pages/shop/prod/web/r1a2b3c4d/blog.cache.json",
    );
  } finally {
    server.close();
  }
});

test("the GCP host's origin invoke answers the optimizer's image path and leaves every other request to Next", async () => {
  const withAssets = newGcpNextHost({
    PORT: "8080",
    OCEL_EDGE_BUCKET: "ocel-prod-edge-k3x9q2",
    OCEL_IMAGE_ENDPOINT: "1",
  });
  const withoutEndpoint = newGcpNextHost({
    PORT: "8080",
    OCEL_EDGE_BUCKET: "ocel-prod-edge-k3x9q2",
  });
  const without = newGcpNextHost({ PORT: "8080" });
  const next = async () => {};

  expect(await withAssets.newOriginInvoke!(next)).not.toBe(next);
  expect(await withoutEndpoint.newOriginInvoke!(next)).toBe(next);
  expect(await without.newOriginInvoke!(next)).toBe(next);
});
