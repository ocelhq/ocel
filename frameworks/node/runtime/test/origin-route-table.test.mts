import type http from "node:http";
import { afterEach, describe, expect, test } from "vitest";
import { serveInvoke, setRequestScope } from "../src/host.mjs";
import { useOriginRouteTable } from "../src/origin-route-table.mjs";
import { newRouteTable, writeRouteTable } from "./route-table-file.mjs";

afterEach(() => {
  useOriginRouteTable({});
  setRequestScope(undefined);
});

type Handler = (req: http.IncomingMessage, res: http.ServerResponse) => void | Promise<void>;

async function serve(
  routes: ReturnType<typeof newRouteTable> | string,
  handler: Handler,
  env: Record<string, string> = {},
): Promise<(path: string, init?: RequestInit) => Promise<Response>> {
  useOriginRouteTable({ OCEL_ROUTE_TABLE: writeRouteTable(routes), ...env });
  let port = 0;
  await serveInvoke(handler, (listening) => {
    port = listening;
  });
  return (path, init) => fetch(`http://127.0.0.1:${port}${path}`, { redirect: "manual", ...init });
}

function page(req: http.IncomingMessage, res: http.ServerResponse): void {
  res.writeHead(200, { "content-type": "text/html" });
  res.end("page");
}

describe("an origin handed a route table", () => {
  test("sets the headers of every rule that matches, a later rule over an earlier one", async () => {
    const get = await serve(
      newRouteTable({
        headers: [
          { src: "/(.*)", headers: { "x-frame-options": "DENY", "x-section": "site" } },
          { src: "/account", headers: { "x-section": "account" } },
        ],
      }),
      page,
    );

    const account = await get("/account");
    const other = await get("/about");

    expect(account.headers.get("x-frame-options")).toBe("DENY");
    expect(account.headers.get("x-section")).toBe("account");
    expect(other.headers.get("x-section")).toBe("site");
  });

  test("sets a header over the one its handler set, and on a response that is not a 200", async () => {
    const get = await serve(
      newRouteTable({ headers: [{ src: "/(.*)", headers: { "x-frame-options": "DENY" } }] }),
      (req, res) => {
        res.writeHead(404, { "x-frame-options": "ALLOWALL" });
        res.end();
      },
    );

    const res = await get("/missing");

    expect(res.status).toBe(404);
    expect(res.headers.get("x-frame-options")).toBe("DENY");
  });

  test("leaves cache-control to the cache rule of a cached route, and sets it on a route that caches nothing", async () => {
    const get = await serve(
      newRouteTable({
        headers: [{ src: "/(.*)", headers: { "cache-control": "public, max-age=3" } }],
      }),
      page,
      { OCEL_RELEASE_TOKEN: "r0a1b2c3d" },
    );

    const cached = await get("/blog/a");
    const uncached = await get("/account");

    expect(cached.headers.get("cache-control")).toBe("s-maxage=60, stale-while-revalidate=540");
    expect(uncached.headers.get("cache-control")).toBe("public, max-age=3");
  });

  test("redirects the first rule that matches, with the groups of its pattern", async () => {
    let reached = false;
    const get = await serve(
      newRouteTable({
        redirects: [
          { src: "/old/(.*)", location: "/new/$1", status: 308 },
          { src: "/old/(.*)", location: "/never/$1", status: 301 },
        ],
      }),
      (req, res) => {
        reached = true;
        page(req, res);
      },
    );

    const res = await get("/old/a/b");

    expect(res.status).toBe(308);
    expect(res.headers.get("location")).toBe("/new/a/b");
    expect(reached).toBe(false);
  });

  test("carries the query of the request over to the location it redirects to", async () => {
    const get = await serve(
      newRouteTable({ redirects: [{ src: "/old", location: "/new", status: 307 }] }),
      page,
    );

    const res = await get("/old?page=2");

    expect(res.headers.get("location")).toBe("/new?page=2");
  });

  test("keeps the query a location states of its own", async () => {
    const get = await serve(
      newRouteTable({ redirects: [{ src: "/old", location: "/new?from=old", status: 307 }] }),
      page,
    );

    const res = await get("/old?page=2");

    expect(res.headers.get("location")).toBe("/new?from=old");
  });

  test("sets the header rules on a redirect it answers", async () => {
    const get = await serve(
      newRouteTable({
        headers: [{ src: "/(.*)", headers: { "x-frame-options": "DENY" } }],
        redirects: [{ src: "/old", location: "/new", status: 301 }],
      }),
      page,
    );

    const res = await get("/old");

    expect(res.headers.get("x-frame-options")).toBe("DENY");
  });

  test("redirects only the paths inside its base path", async () => {
    const get = await serve(
      newRouteTable({
        basePath: "/docs",
        redirects: [{ src: "/old", location: "/docs/new", status: 308 }],
      }),
      page,
    );

    expect((await get("/docs/old")).status).toBe(308);
    expect((await get("/old")).status).toBe(200);
  });

  test("redirects a path to the form its trailing slash mode serves, keeping its query", async () => {
    const never = await serve(newRouteTable({ trailingSlash: "never" }), page);
    const removed = await never("/blog/a/?x=1");
    expect(removed.status).toBe(308);
    expect(removed.headers.get("location")).toBe("/blog/a?x=1");
    expect((await never("/blog/a")).status).toBe(200);
    expect((await never("/")).status).toBe(200);

    const always = await serve(newRouteTable({ trailingSlash: "always" }), page);
    const added = await always("/blog/a");
    expect(added.status).toBe(308);
    expect(added.headers.get("location")).toBe("/blog/a/");
    expect((await always("/blog/a/")).status).toBe(200);
    expect((await always("/logo.svg")).status).toBe(200);
  });

  test("redirects a path that begins with two slashes to a location on its own site", async () => {
    const never = await serve(newRouteTable({ trailingSlash: "never" }), page);
    const always = await serve(newRouteTable({ trailingSlash: "always" }), page);
    const substituted = await serve(
      newRouteTable({ redirects: [{ src: "/(.*)", location: "/$1", status: 307 }] }),
      page,
    );

    expect((await never("//evil.com/")).headers.get("location")).toBe("/evil.com");
    expect((await always("//evil")).headers.get("location")).toBe("/evil/");
    expect((await substituted("//evil.com")).headers.get("location")).toBe("/evil.com");
  });

  test("redirects no write for its trailing slash, which would drop the body", async () => {
    const get = await serve(newRouteTable({ trailingSlash: "never" }), page);

    const res = await get("/blog/a/", { method: "POST" });

    expect(res.status).toBe(200);
  });

  test("hands a request no rule names to the handler untouched", async () => {
    const get = await serve(newRouteTable(), page);

    const res = await get("/anything");

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("page");
  });

  test("refuses to boot when the table cannot be read", () => {
    expect(() => useOriginRouteTable({ OCEL_ROUTE_TABLE: writeRouteTable("{") })).toThrow(
      /route table/,
    );
    expect(() =>
      useOriginRouteTable({ OCEL_ROUTE_TABLE: "/nowhere/ocel-route-table.json" }),
    ).toThrow(/OCEL_ROUTE_TABLE/);
  });
});
