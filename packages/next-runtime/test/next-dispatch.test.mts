import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
import { afterEach, expect, test, vi } from "vitest";

const dispatchPath = fileURLToPath(
  new URL("../src/next-dispatch.cjs", import.meta.url),
);
const require = createRequire(import.meta.url);
const createDispatch = require(dispatchPath);

const ENTRIES = {
  "app/page": "./.next/server/app/page.js",
  "app/api/x/route": "./.next/server/app/api/x/route.js",
};

function fakeLoader() {
  const loads: string[] = [];
  const load = (path: string) => {
    loads.push(path);
    return { handler: (...args: unknown[]) => ({ path, args }) };
  };
  return { loads, load };
}

function throwingLoader(broken: string, error = new Error("boom")) {
  const loads: string[] = [];
  const load = (path: string) => {
    loads.push(path);
    if (path === broken) throw error;
    return { handler: (...args: unknown[]) => ({ path, args }) };
  };
  return { loads, load };
}

function silenceErrors() {
  return vi.spyOn(console, "error").mockImplementation(() => {});
}

afterEach(() => {
  vi.restoreAllMocks();
});

function fakeReq(entry?: string) {
  return { headers: entry === undefined ? {} : { "x-ocel-entry": entry } };
}

function fakeRes() {
  return {
    statusCode: 200,
    body: undefined as unknown,
    end(chunk?: unknown) {
      this.body = chunk;
    },
  };
}

test("loads the primary entry once while the launcher is still being required", () => {
  const { loads, load } = fakeLoader();

  createDispatch({ entries: ENTRIES, primary: "app/page", load });

  expect(loads).toEqual([ENTRIES["app/page"]]);
});

test("loads nothing eagerly without a primary", () => {
  const { loads, load } = fakeLoader();

  createDispatch({ entries: ENTRIES, primary: null, load });

  expect(loads).toEqual([]);
});

test("loads a lazy entry on its first request and never again", async () => {
  const { loads, load } = fakeLoader();
  const dispatch = createDispatch({ entries: ENTRIES, primary: null, load });

  await dispatch.handler(fakeReq("app/api/x/route"), fakeRes(), {});
  await dispatch.handler(fakeReq("app/api/x/route"), fakeRes(), {});

  expect(loads).toEqual([ENTRIES["app/api/x/route"]]);
});

test("reuses the eagerly loaded primary when a request hits it", async () => {
  const { loads, load } = fakeLoader();
  const dispatch = createDispatch({ entries: ENTRIES, primary: "app/page", load });

  await dispatch.handler(fakeReq("app/page"), fakeRes(), {});

  expect(loads).toEqual([ENTRIES["app/page"]]);
});

test("passes req, res and ctx through to the entry untouched", async () => {
  const seen: unknown[] = [];
  const dispatch = createDispatch({
    entries: ENTRIES,
    primary: null,
    load: () => ({
      handler: (...args: unknown[]) => {
        seen.push(args);
        return "returned";
      },
    }),
  });
  const req = fakeReq("app/page");
  const res = fakeRes();
  const ctx = { waitUntil: () => {}, requestMeta: {} };

  const result = await dispatch.handler(req, res, ctx);

  expect(result).toBe("returned");
  expect(seen).toEqual([[req, res, ctx]]);
  expect(res.statusCode).toBe(200);
});

test("fails closed on a request with no entry header", async () => {
  const { load } = fakeLoader();
  const dispatch = createDispatch({ entries: ENTRIES, primary: null, load });
  const res = fakeRes();

  await dispatch.handler(fakeReq(), res, {});

  expect(res.statusCode).toBe(502);
  expect(res.body).toMatch(/x-ocel-entry/);
});

test("fails closed on a key the bundle does not carry, naming it", async () => {
  const { loads, load } = fakeLoader();
  const dispatch = createDispatch({ entries: ENTRIES, primary: null, load });
  const res = fakeRes();

  await dispatch.handler(fakeReq("app/ghost/page"), res, {});

  expect(res.statusCode).toBe(502);
  expect(res.body).toMatch(/app\/ghost\/page/);
  expect(loads).toEqual([]);
});

test("fails closed on an entry module that exports no handler, naming the key", async () => {
  const dispatch = createDispatch({
    entries: ENTRIES,
    primary: null,
    load: () => ({}),
  });
  const res = fakeRes();

  await dispatch.handler(fakeReq("app/page"), res, {});

  expect(res.statusCode).toBe(502);
  expect(res.body).toMatch(/app\/page/);
});

test("a primary that throws on load neither kills the factory nor the other entries", async () => {
  const errors = silenceErrors();
  const { load } = throwingLoader(ENTRIES["app/page"]);

  const dispatch = createDispatch({ entries: ENTRIES, primary: "app/page", load });
  const res = fakeRes();
  const result = await dispatch.handler(fakeReq("app/api/x/route"), res, {});

  expect(res.statusCode).toBe(200);
  expect(result).toMatchObject({ path: ENTRIES["app/api/x/route"] });
  expect(errors).toHaveBeenCalled();
  expect(String(errors.mock.calls[0]?.[0])).toMatch(/app\/page/);
});

test("the broken primary's own key fails closed, naming it", async () => {
  silenceErrors();
  const { load } = throwingLoader(ENTRIES["app/page"]);
  const dispatch = createDispatch({ entries: ENTRIES, primary: "app/page", load });
  const res = fakeRes();

  await dispatch.handler(fakeReq("app/page"), res, {});

  expect(res.statusCode).toBe(502);
  expect(res.body).toMatch(/app\/page/);
  expect(res.body).toMatch(/boom/);
});

test("a lazy entry that throws on load fails closed instead of propagating", async () => {
  silenceErrors();
  const { load } = throwingLoader(ENTRIES["app/api/x/route"]);
  const dispatch = createDispatch({ entries: ENTRIES, primary: null, load });
  const res = fakeRes();

  await dispatch.handler(fakeReq("app/api/x/route"), res, {});

  expect(res.statusCode).toBe(502);
  expect(res.body).toMatch(/app\/api\/x\/route/);
});

test("memoizes a failed load so a doomed require is never re-attempted", async () => {
  silenceErrors();
  const { loads, load } = throwingLoader(ENTRIES["app/api/x/route"]);
  const dispatch = createDispatch({ entries: ENTRIES, primary: null, load });

  await dispatch.handler(fakeReq("app/api/x/route"), fakeRes(), {});
  const second = fakeRes();
  await dispatch.handler(fakeReq("app/api/x/route"), second, {});

  expect(loads).toEqual([ENTRIES["app/api/x/route"]]);
  expect(second.statusCode).toBe(502);
  expect(second.body).toMatch(/app\/api\/x\/route/);
});

test("memoizes a failed prime so the broken primary is required only at init", async () => {
  silenceErrors();
  const { loads, load } = throwingLoader(ENTRIES["app/page"]);
  const dispatch = createDispatch({ entries: ENTRIES, primary: "app/page", load });

  await dispatch.handler(fakeReq("app/page"), fakeRes(), {});
  await dispatch.handler(fakeReq("app/page"), fakeRes(), {});

  expect(loads).toEqual([ENTRIES["app/page"]]);
});
