import { afterEach, expect, test, vi } from "vitest";

// Everything the handler decides — budget, per-entry cap, the LRU, the tag map —
// lives in module scope, exactly as it does in a warm Lambda. Each test therefore
// rebuilds the module graph so one test's cache never leaks into the next.
async function loadHandler(env: Record<string, string> = {}) {
  vi.resetModules();
  for (const [k, v] of Object.entries(env)) process.env[k] = v;
  return (await import("../src/next/use-cache-default.mjs")).default;
}

const budgetVars = [
  "OCEL_USE_CACHE_MAX_BYTES",
  "OCEL_USE_CACHE_MAX_ENTRY",
  "AWS_LAMBDA_FUNCTION_MEMORY_SIZE",
];

afterEach(() => {
  for (const v of budgetVars) delete process.env[v];
});

function streamOf(body: string): ReadableStream<Uint8Array> {
  return new ReadableStream({
    start(controller) {
      controller.enqueue(new Uint8Array(Buffer.from(body)));
      controller.close();
    },
  });
}

function entry(body: string, over: Record<string, unknown> = {}) {
  return {
    value: streamOf(body),
    tags: [],
    stale: 0,
    timestamp: Date.now(),
    expire: 3600,
    revalidate: 60,
    ...over,
  };
}

async function readAll(stream: ReadableStream<Uint8Array>): Promise<string> {
  const reader = stream.getReader();
  let out = "";
  for (let chunk; !(chunk = await reader.read()).done; ) {
    out += Buffer.from(chunk.value).toString();
  }
  return out;
}

test("serves a stored entry back on a warm instance", async () => {
  const handler = await loadHandler();

  await handler.set("k", Promise.resolve(entry("payload")));
  const hit = await handler.get("k", []);

  expect(hit).toBeDefined();
  expect(await readAll(hit!.value)).toBe("payload");
});

test("misses a key that was never stored", async () => {
  const handler = await loadHandler();

  expect(await handler.get("absent", [])).toBeUndefined();
});

// The stored entry's stream is one-shot: a handler that handed the same stream
// out twice would turn every read after the first into a miss.
test("rebuilds the value stream on every read", async () => {
  const handler = await loadHandler();

  await handler.set("k", Promise.resolve(entry("payload")));

  expect(await readAll((await handler.get("k", []))!.value)).toBe("payload");
  expect(await readAll((await handler.get("k", []))!.value)).toBe("payload");
});

test("a read arriving during an in-flight set waits for it and hits", async () => {
  const handler = await loadHandler();

  let complete: (e: unknown) => void = () => {};
  const pending = new Promise<any>((resolve) => {
    complete = resolve;
  });

  const setting = handler.set("k", pending);
  const reading = handler.get("k", []);
  complete(entry("payload"));
  await setting;

  const hit = await reading;
  expect(hit).toBeDefined();
  expect(await readAll(hit!.value)).toBe("payload");
});

test("evicts least-recently-used entries once the byte budget is exceeded", async () => {
  const handler = await loadHandler({ OCEL_USE_CACHE_MAX_BYTES: "300" });
  const body = "x".repeat(100);

  await handler.set("a", Promise.resolve(entry(body)));
  await handler.set("b", Promise.resolve(entry(body)));
  await handler.set("c", Promise.resolve(entry(body)));

  // Reading `a` makes `b` the least recently used.
  expect(await handler.get("a", [])).toBeDefined();

  await handler.set("d", Promise.resolve(entry(body)));

  expect(await handler.get("b", [])).toBeUndefined();
  expect(await handler.get("a", [])).toBeDefined();
  expect(await handler.get("c", [])).toBeDefined();
  expect(await handler.get("d", [])).toBeDefined();
});

test("refuses an entry above the per-entry cap without evicting anything", async () => {
  const handler = await loadHandler({
    OCEL_USE_CACHE_MAX_BYTES: "10000",
    OCEL_USE_CACHE_MAX_ENTRY: "50",
  });

  await handler.set("small", Promise.resolve(entry("tiny")));
  await handler.set("huge", Promise.resolve(entry("x".repeat(500))));

  expect(await handler.get("huge", [])).toBeUndefined();
  expect(await handler.get("small", [])).toBeDefined();
});

test("derives the byte budget from the function's configured memory", async () => {
  // 1MB of memory yields a ~104KB budget, so a 60KB entry evicts its predecessor.
  const handler = await loadHandler({ AWS_LAMBDA_FUNCTION_MEMORY_SIZE: "1" });
  const body = "x".repeat(60 * 1024);

  await handler.set("a", Promise.resolve(entry(body)));
  await handler.set("b", Promise.resolve(entry(body)));

  expect(await handler.get("a", [])).toBeUndefined();
  expect(await handler.get("b", [])).toBeDefined();
});

test("leaves no entry behind when the value stream errors part-way", async () => {
  const handler = await loadHandler();

  const torn = new ReadableStream<Uint8Array>({
    start(controller) {
      controller.enqueue(new Uint8Array(Buffer.from("half")));
      controller.error(new Error("render blew up"));
    },
  });

  await expect(
    handler.set("k", Promise.resolve(entry("", { value: torn }))),
  ).resolves.toBeUndefined();
  expect(await handler.get("k", [])).toBeUndefined();
});

test("survives a pending entry that never materialises", async () => {
  const handler = await loadHandler();

  await expect(
    handler.set("k", Promise.reject(new Error("render blew up"))),
  ).resolves.toBeUndefined();
  expect(await handler.get("k", [])).toBeUndefined();
});

test("misses an entry past its revalidate duration", async () => {
  const handler = await loadHandler();

  await handler.set(
    "k",
    Promise.resolve(entry("payload", { timestamp: Date.now() - 10_000, revalidate: 5 })),
  );

  expect(await handler.get("k", [])).toBeUndefined();
});

test("misses an entry whose explicit tag was revalidated", async () => {
  const handler = await loadHandler();

  await handler.set(
    "k",
    Promise.resolve(entry("payload", { tags: ["products"], timestamp: Date.now() - 1_000 })),
  );
  await handler.updateTags(["products"]);

  expect(await handler.get("k", [])).toBeUndefined();
});

test("leaves an entry whose tags were untouched alone", async () => {
  const handler = await loadHandler();

  await handler.set(
    "k",
    Promise.resolve(entry("payload", { tags: ["products"], timestamp: Date.now() - 1_000 })),
  );
  await handler.updateTags(["reviews"]);

  expect(await handler.get("k", [])).toBeDefined();
});

// A revalidation carrying durations but no expire marks the tag stale only.
// Next reads revalidate === -1 as "serve this, then regenerate".
test("serves a tag-stale entry with the revalidate signal", async () => {
  const handler = await loadHandler();

  await handler.set(
    "k",
    Promise.resolve(entry("payload", { tags: ["products"], timestamp: Date.now() - 1_000 })),
  );
  await handler.updateTags(["products"], {});

  const hit = await handler.get("k", []);
  expect(hit).toBeDefined();
  expect(hit!.revalidate).toBe(-1);
  expect(await readAll(hit!.value)).toBe("payload");
});

test("reports the latest expiry per tag and zero for tags never revalidated", async () => {
  const handler = await loadHandler();

  expect(await handler.getExpiration(["products"])).toBe(0);

  const before = Date.now();
  await handler.updateTags(["products"]);

  const expiration = await handler.getExpiration(["products", "reviews"]);
  expect(expiration).toBeGreaterThanOrEqual(before);
  expect(expiration).toBeLessThanOrEqual(Date.now() + 1);
});

test("does not treat a duration-scoped revalidation as an expiry until it is asked for", async () => {
  const handler = await loadHandler();

  await handler.updateTags(["products"], {});

  // stale was recorded, expired was not — so nothing has an expiry yet.
  expect(await handler.getExpiration(["products"])).toBe(0);
});

test("keeps an earlier stale marker when a later revalidation only sets an expiry", async () => {
  const handler = await loadHandler();

  await handler.updateTags(["products"], {});
  await handler.updateTags(["products"], { expire: 60 });

  const expiration = await handler.getExpiration(["products"]);
  expect(expiration).toBeGreaterThan(Date.now());
});

test("refreshTags resolves without a durable backend", async () => {
  const handler = await loadHandler();

  await expect(handler.refreshTags()).resolves.toBeUndefined();
});
