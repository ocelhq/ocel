import { createRequire } from "node:module";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { afterEach, expect, test, vi } from "vitest";

const handlerPath = fileURLToPath(
  new URL("../src/edge-cache-handler.cjs", import.meta.url),
);
const require = createRequire(import.meta.url);

// The handler branches at module scope, so each tier has to be loaded under its
// own NEXT_RUNTIME — which in a bundled edge chunk is a build-time constant and
// here is a fresh require.
function loadHandler(runtime?: string) {
  if (runtime) vi.stubEnv("NEXT_RUNTIME", runtime);
  else vi.stubEnv("NEXT_RUNTIME", "nodejs");
  delete require.cache[handlerPath];
  return require(handlerPath);
}

function fakeRpc() {
  return {
    entry: null as unknown,
    calls: [] as { method: string; args: unknown[] }[],
    async fetchGet(...args: unknown[]) {
      this.calls.push({ method: "fetchGet", args });
      return this.entry;
    },
    async fetchSet(...args: unknown[]) {
      this.calls.push({ method: "fetchSet", args });
    },
    async revalidateTags(...args: unknown[]) {
      this.calls.push({ method: "revalidateTags", args });
    },
  };
}

function bind(rpc: unknown, scope = "prod/app/build") {
  (globalThis as Record<string, unknown>).__OCEL_EDGE_CACHE = { rpc, scope };
}

afterEach(() => {
  vi.unstubAllEnvs();
  delete (globalThis as Record<string, unknown>).__OCEL_EDGE_CACHE;
  delete require.cache[handlerPath];
});

test("delegates to FileSystemCache off the edge, so the build keeps its cache", () => {
  const Handler = loadHandler();

  expect(Handler.name).toBe("FileSystemCache");
});

// What keeps the Node builtins FileSystemCache reaches for out of every edge
// chunk is that the require sits in the branch Turbopack eliminates.
test("holds no require in the edge branch", () => {
  const source = readFileSync(handlerPath, "utf8");
  const [edgeBranch] = source.split("\n} else {");

  expect(source).toContain('process.env.NEXT_RUNTIME === "edge"');
  expect(edgeBranch).not.toContain("require(");
});

test("reads a fetch entry through the binding, keyed by scope", async () => {
  const Handler = loadHandler("edge");
  const rpc = fakeRpc();
  rpc.entry = { lastModified: 5, value: { kind: "FETCH", data: { x: 1 } } };
  bind(rpc);

  const entry = await new Handler().get("k", {
    kind: "FETCH",
    tags: ["a"],
    softTags: ["b"],
  });

  expect(entry).toEqual(rpc.entry);
  expect(rpc.calls[0]).toEqual({
    method: "fetchGet",
    args: ["prod/app/build", "k", ["a", "b"]],
  });
});

// Next does not wrap get() in a try/catch, so a throw would surface as a render
// error rather than a miss.
test("reports a miss when the binding throws", async () => {
  const Handler = loadHandler("edge");
  bind({
    async fetchGet() {
      throw new Error("upstream down");
    },
  });

  await expect(new Handler().get("k", { kind: "FETCH" })).resolves.toBeNull();
});

test("reports a miss for a kind the edge never stores", async () => {
  const Handler = loadHandler("edge");
  const rpc = fakeRpc();
  bind(rpc);

  await expect(new Handler().get("k", { kind: "APP_PAGE" })).resolves.toBeNull();
  expect(rpc.calls).toEqual([]);
});

test("writes a fetch entry stamped with its own tags and lastModified", async () => {
  const Handler = loadHandler("edge");
  const rpc = fakeRpc();
  bind(rpc);
  vi.spyOn(Date, "now").mockReturnValue(1234);

  await new Handler().set(
    "k",
    { kind: "FETCH", data: { body: "hi" }, revalidate: 30 },
    { tags: ["a"] },
  );

  expect(rpc.calls[0]).toEqual({
    method: "fetchSet",
    args: [
      "prod/app/build",
      "k",
      {
        lastModified: 1234,
        value: {
          kind: "FETCH",
          data: { body: "hi" },
          revalidate: 30,
          tags: ["a"],
        },
      },
      ["a"],
    ],
  });
});

// The write is Next's to await — it hands a background revalidation's set() to
// evt.waitUntil, which settles it after the response. Detaching it here would
// instead have the request's cancellation kill it.
test("hands the write's promise back rather than detaching it", async () => {
  const Handler = loadHandler("edge");
  let settle: () => void;
  const landed = new Promise<void>((resolve) => (settle = resolve));
  bind({ fetchSet: () => landed });

  const pending = new Handler().set("k", { kind: "FETCH", data: {} }, {});
  let done = false;
  void pending.then(() => (done = true));
  await Promise.resolve();
  expect(done).toBe(false);

  settle!();
  await pending;
  expect(done).toBe(true);
});

// Next's node entry templates ride along in every edge chunk as dead code, so a
// page-kind write is reachable in principle. IncrementalCache.set catches and
// warns, which makes a throw the loud-but-survivable option.
test("refuses a non-fetch write instead of storing it", async () => {
  const Handler = loadHandler("edge");
  const rpc = fakeRpc();
  bind(rpc);

  await expect(
    new Handler().set("k", { kind: "APP_PAGE", html: "<p/>" }, {}),
  ).rejects.toThrow(/fetch entries only.*APP_PAGE/);
  expect(rpc.calls).toEqual([]);
});

test("forwards a tag invalidation with its durations", async () => {
  const Handler = loadHandler("edge");
  const rpc = fakeRpc();
  bind(rpc);

  await new Handler().revalidateTag("a", { expire: 60 });
  await new Handler().revalidateTag(["b", "c"]);
  await new Handler().revalidateTag([]);

  expect(rpc.calls).toEqual([
    {
      method: "revalidateTags",
      args: ["prod/app/build", ["a"], { expire: 60 }],
    },
    { method: "revalidateTags", args: ["prod/app/build", ["b", "c"], undefined] },
  ]);
});

// A missing binding means the shim never ran or the worker passed no cache
// entrypoint; silently dropping writes would leave an app that looks cached and
// never is.
test("fails loudly on a write with no binding", async () => {
  const Handler = loadHandler("edge");

  await expect(
    new Handler().set("k", { kind: "FETCH", data: {} }, {}),
  ).rejects.toThrow(/__OCEL_EDGE_CACHE/);
  await expect(new Handler().revalidateTag("a")).rejects.toThrow(
    /__OCEL_EDGE_CACHE/,
  );
});
