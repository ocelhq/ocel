import { describe, expect, it } from "vitest";

import {
  canonicalPathname,
  middlewarePathname,
  routingPathname,
  type TrailingSlashConfig,
} from "../src/trailing-slash";

const BUILD_ID = "t";

// One row per pathname shape, per config. `canonical` is the form the client must
// be on, `routing` the form the build's pathnames are keyed by, `redirects`
// whether serve() would answer a 308 (i.e. canonical !== the requested path), and
// `middleware` the URL middleware is handed for the *routing* form.
interface Row {
  what: string;
  path: string;
  canonical: string;
  routing: string;
  redirects: boolean;
  middleware: string;
  isDataRequest?: boolean;
}

function check(config: TrailingSlashConfig, rows: Row[]) {
  it.each(rows)("$what: $path", (row) => {
    expect(canonicalPathname(row.path, config, row.isDataRequest ?? false)).toBe(
      row.canonical,
    );
    expect(routingPathname(row.path)).toBe(row.routing);
    expect(canonicalPathname(row.path, config, row.isDataRequest ?? false) !== row.path).toBe(
      row.redirects,
    );
    expect(middlewarePathname(row.routing, config, BUILD_ID)).toBe(row.middleware);
  });
}

// Next compiles the basePath into the source of every internal rule, so a path
// that is not under basePath matches none of them and Next 404s it as it stands.
// The boundary is a segment: /docsy is not under /docs. Nothing here may be
// touched, whatever trailingSlash says — a redirect would both regress a correct
// 404 and tell a prober what the app's basePath is.
const offBasePath: Row[] = ["/", "/favicon.ico", "/wp-admin", "/foo", "/docsy/page"].map(
  (path) => ({
    what: "off basePath, untouched",
    path,
    canonical: path,
    routing: path,
    redirects: false,
    middleware: path,
  }),
);

offBasePath.push({
  // Not even the trailingSlash: false generic strip reaches off-basePath paths.
  what: "off basePath, a trailing slash is not stripped either",
  path: "/docsy/page/",
  canonical: "/docsy/page/",
  routing: "/docsy/page",
  redirects: false,
  middleware: "/docsy/page",
});

describe("trailingSlash: true", () => {
  const config: TrailingSlashConfig = { basePath: "", trailingSlash: true };

  check(config, [
    {
      what: "root",
      path: "/",
      canonical: "/",
      routing: "/",
      redirects: false,
      middleware: "/",
    },
    {
      what: "static page, slash-free",
      path: "/a",
      canonical: "/a/",
      routing: "/a",
      redirects: true,
      middleware: "/a/",
    },
    {
      what: "static page, canonical",
      path: "/a/",
      canonical: "/a/",
      routing: "/a",
      redirects: false,
      middleware: "/a/",
    },
    {
      what: "dynamic page, slash-free",
      path: "/blog/post",
      canonical: "/blog/post/",
      routing: "/blog/post",
      redirects: true,
      middleware: "/blog/post/",
    },
    {
      what: "dynamic page, canonical",
      path: "/blog/post/",
      canonical: "/blog/post/",
      routing: "/blog/post",
      redirects: false,
      middleware: "/blog/post/",
    },
    {
      what: "file extension is left alone",
      path: "/next.svg",
      canonical: "/next.svg",
      routing: "/next.svg",
      redirects: false,
      middleware: "/next.svg",
    },
    {
      what: "file extension with a slash is stripped",
      path: "/next.svg/",
      canonical: "/next.svg",
      routing: "/next.svg",
      redirects: true,
      middleware: "/next.svg",
    },
    {
      what: "a dot in a non-final segment does not exempt the path",
      path: "/v1.0/docs",
      canonical: "/v1.0/docs/",
      routing: "/v1.0/docs",
      redirects: true,
      middleware: "/v1.0/docs/",
    },
    {
      what: "build asset",
      path: "/_next/static/chunks/a.js",
      canonical: "/_next/static/chunks/a.js",
      routing: "/_next/static/chunks/a.js",
      redirects: false,
      middleware: "/_next/static/chunks/a.js",
    },
    {
      what: "data request becomes the page middleware sees",
      path: "/_next/data/t/a.json",
      canonical: "/_next/data/t/a.json",
      routing: "/_next/data/t/a.json",
      redirects: false,
      middleware: "/a/",
      isDataRequest: true,
    },
    {
      what: "data request for the index page",
      path: "/_next/data/t/index.json",
      canonical: "/_next/data/t/index.json",
      routing: "/_next/data/t/index.json",
      redirects: false,
      middleware: "/",
      isDataRequest: true,
    },
    {
      what: "data request for a nested page",
      path: "/_next/data/t/blog/post.json",
      canonical: "/_next/data/t/blog/post.json",
      routing: "/_next/data/t/blog/post.json",
      redirects: false,
      middleware: "/blog/post/",
      isDataRequest: true,
    },
    {
      what: "a slashed data path is not stripped when x-nextjs-data is set",
      path: "/_next/data/t/a.json/",
      canonical: "/_next/data/t/a.json/",
      routing: "/_next/data/t/a.json",
      redirects: false,
      middleware: "/a/",
      isDataRequest: true,
    },
    {
      what: "the same path without x-nextjs-data is stripped",
      path: "/_next/data/t/a.json/",
      canonical: "/_next/data/t/a.json",
      routing: "/_next/data/t/a.json",
      redirects: true,
      middleware: "/a/",
    },
    {
      what: "well-known is exempt",
      path: "/.well-known/acme",
      canonical: "/.well-known/acme",
      routing: "/.well-known/acme",
      redirects: false,
      middleware: "/.well-known/acme",
    },
    {
      what: "well-known with a slash is exempt from the strip too",
      path: "/.well-known/acme/",
      canonical: "/.well-known/acme/",
      routing: "/.well-known/acme",
      redirects: false,
      middleware: "/.well-known/acme",
    },
    {
      what: "an api route is a page like any other",
      path: "/api/hello",
      canonical: "/api/hello/",
      routing: "/api/hello",
      redirects: true,
      middleware: "/api/hello/",
    },
  ]);
});

describe("trailingSlash: true, basePath: /docs", () => {
  const config: TrailingSlashConfig = { basePath: "/docs", trailingSlash: true };

  check(config, [
    {
      what: "the bare basePath gains a slash",
      path: "/docs",
      canonical: "/docs/",
      routing: "/docs",
      redirects: true,
      middleware: "/docs/",
    },
    {
      what: "the basePath root is canonical",
      path: "/docs/",
      canonical: "/docs/",
      routing: "/docs",
      redirects: false,
      middleware: "/docs/",
    },
    {
      what: "a page under basePath",
      path: "/docs/hello",
      canonical: "/docs/hello/",
      routing: "/docs/hello",
      redirects: true,
      middleware: "/docs/hello/",
    },
    {
      what: "a canonical page under basePath",
      path: "/docs/hello/",
      canonical: "/docs/hello/",
      routing: "/docs/hello",
      redirects: false,
      middleware: "/docs/hello/",
    },
    {
      what: "a data request under basePath",
      path: "/docs/_next/data/t/hello.json",
      canonical: "/docs/_next/data/t/hello.json",
      routing: "/docs/_next/data/t/hello.json",
      redirects: false,
      middleware: "/docs/hello/",
      isDataRequest: true,
    },
    {
      what: "the index data request under basePath is the basePath root",
      path: "/docs/_next/data/t/index.json",
      canonical: "/docs/_next/data/t/index.json",
      routing: "/docs/_next/data/t/index.json",
      redirects: false,
      middleware: "/docs/",
      isDataRequest: true,
    },
    {
      what: "well-known is measured after basePath removal",
      path: "/docs/.well-known/acme",
      canonical: "/docs/.well-known/acme",
      routing: "/docs/.well-known/acme",
      redirects: false,
      middleware: "/docs/.well-known/acme",
    },
    ...offBasePath,
  ]);
});

describe("trailingSlash: false", () => {
  const config: TrailingSlashConfig = { basePath: "" };

  check(config, [
    {
      what: "root",
      path: "/",
      canonical: "/",
      routing: "/",
      redirects: false,
      middleware: "/",
    },
    {
      what: "static page",
      path: "/a",
      canonical: "/a",
      routing: "/a",
      redirects: false,
      middleware: "/a",
    },
    {
      what: "static page with a slash",
      path: "/a/",
      canonical: "/a",
      routing: "/a",
      redirects: true,
      middleware: "/a",
    },
    {
      what: "dynamic page with a slash",
      path: "/blog/post/",
      canonical: "/blog/post",
      routing: "/blog/post",
      redirects: true,
      middleware: "/blog/post",
    },
    {
      what: "file extension",
      path: "/next.svg",
      canonical: "/next.svg",
      routing: "/next.svg",
      redirects: false,
      middleware: "/next.svg",
    },
    {
      what: "the strip rule is generic, so a file extension is stripped too",
      path: "/next.svg/",
      canonical: "/next.svg",
      routing: "/next.svg",
      redirects: true,
      middleware: "/next.svg",
    },
    {
      what: "build asset",
      path: "/_next/static/chunks/a.js",
      canonical: "/_next/static/chunks/a.js",
      routing: "/_next/static/chunks/a.js",
      redirects: false,
      middleware: "/_next/static/chunks/a.js",
    },
    {
      what: "build asset with a slash is stripped, unlike under trailingSlash: true",
      path: "/_next/static/chunks/a.js/",
      canonical: "/_next/static/chunks/a.js",
      routing: "/_next/static/chunks/a.js",
      redirects: true,
      middleware: "/_next/static/chunks/a.js",
    },
    {
      what: "data request",
      path: "/_next/data/t/a.json",
      canonical: "/_next/data/t/a.json",
      routing: "/_next/data/t/a.json",
      redirects: false,
      middleware: "/a",
      isDataRequest: true,
    },
    {
      what: "index data request",
      path: "/_next/data/t/index.json",
      canonical: "/_next/data/t/index.json",
      routing: "/_next/data/t/index.json",
      redirects: false,
      middleware: "/",
      isDataRequest: true,
    },
    {
      what: "well-known has no exemption under trailingSlash: false",
      path: "/.well-known/acme",
      canonical: "/.well-known/acme",
      routing: "/.well-known/acme",
      redirects: false,
      middleware: "/.well-known/acme",
    },
    {
      what: "api route",
      path: "/api/hello",
      canonical: "/api/hello",
      routing: "/api/hello",
      redirects: false,
      middleware: "/api/hello",
    },
  ]);
});

describe("trailingSlash: false, basePath: /docs", () => {
  const config: TrailingSlashConfig = { basePath: "/docs" };

  check(config, [
    {
      what: "the bare basePath is canonical",
      path: "/docs",
      canonical: "/docs",
      routing: "/docs",
      redirects: false,
      middleware: "/docs",
    },
    {
      what: "the basePath root loses its slash",
      path: "/docs/",
      canonical: "/docs",
      routing: "/docs",
      redirects: true,
      middleware: "/docs",
    },
    {
      what: "a page under basePath",
      path: "/docs/hello/",
      canonical: "/docs/hello",
      routing: "/docs/hello",
      redirects: true,
      middleware: "/docs/hello",
    },
    {
      what: "a data request under basePath",
      path: "/docs/_next/data/t/hello.json",
      canonical: "/docs/_next/data/t/hello.json",
      routing: "/docs/_next/data/t/hello.json",
      redirects: false,
      middleware: "/docs/hello",
      isDataRequest: true,
    },
    ...offBasePath,
  ]);
});

// skipTrailingSlashRedirect suppresses the 308 and nothing else: the routing-form
// strip stays unconditional, or a canonical `/a/` resolves to no pathname the
// build emitted and 404s.
describe("skipTrailingSlashRedirect", () => {
  for (const trailingSlash of [true, false]) {
    describe(`trailingSlash: ${trailingSlash}`, () => {
      const config: TrailingSlashConfig = {
        basePath: "",
        trailingSlash,
        skipTrailingSlashRedirect: true,
      };

      it.each(["/", "/a", "/a/", "/blog/post", "/blog/post/", "/next.svg", "/next.svg/"])(
        "issues no redirect for %s",
        (path) => {
          expect(canonicalPathname(path, config)).toBe(path);
        },
      );

      it("still strips the routing form", () => {
        expect(routingPathname("/a/")).toBe("/a");
        expect(routingPathname("/")).toBe("/");
      });

      it("still resolves a data request to its page for middleware", () => {
        expect(middlewarePathname("/_next/data/t/a.json", config, BUILD_ID)).toBe(
          trailingSlash ? "/a/" : "/a",
        );
      });
    });
  }
});

// skipMiddlewareUrlNormalize means the app wants the URL as routed, so the data
// rewrite and the slash re-add both stand down.
describe("skipMiddlewareUrlNormalize", () => {
  const config: TrailingSlashConfig = {
    basePath: "",
    trailingSlash: true,
    skipMiddlewareUrlNormalize: true,
  };

  it.each(["/", "/a", "/next.svg", "/_next/data/t/a.json"])(
    "hands middleware %s unchanged",
    (path) => {
      expect(middlewarePathname(path, config, BUILD_ID)).toBe(path);
    },
  );
});
