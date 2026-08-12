import { describe, expect, it } from "vitest";

import {
  canonicalPathname,
  middlewareMatchPathname,
  middlewarePathname,
  needsSlashNormalization,
  normalizeRepeatedSlashes,
  routingPathname,
  type TrailingSlashConfig,
} from "../src/trailing-slash";

const BUILD_ID = "t";

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

describe("the URL middleware is handed, per skipMiddlewareUrlNormalize", () => {
  const cases: {
    what: string;
    path: string;
    normalized: Record<"true" | "false", string>;
    matchedWithFlag?: Record<"true" | "false", string>;
  }[] = [
    { what: "the root", path: "/", normalized: { true: "/", false: "/" } },
    { what: "a slash-free page", path: "/a", normalized: { true: "/a/", false: "/a" } },
    { what: "a slashed page", path: "/a/", normalized: { true: "/a/", false: "/a" } },
    {
      what: "a nested page",
      path: "/blog/post",
      normalized: { true: "/blog/post/", false: "/blog/post" },
    },
    {
      what: "a dotted path",
      path: "/next.svg",
      normalized: { true: "/next.svg", false: "/next.svg" },
    },
    {
      what: "a well-known path",
      path: "/.well-known/acme",
      normalized: { true: "/.well-known/acme", false: "/.well-known/acme" },
    },
    {
      what: "a data request",
      path: "/_next/data/t/a.json",
      normalized: { true: "/a/", false: "/a" },
      matchedWithFlag: { true: "/a", false: "/a" },
    },
    {
      what: "a nested data request",
      path: "/_next/data/t/blog/post.json",
      normalized: { true: "/blog/post/", false: "/blog/post" },
      matchedWithFlag: { true: "/blog/post", false: "/blog/post" },
    },
    {
      what: "an index data request",
      path: "/_next/data/t/index.json",
      normalized: { true: "/", false: "/" },
      matchedWithFlag: { true: "/", false: "/" },
    },
    {
      what: "a locale-prefixed data request",
      path: "/_next/data/t/ja-jp/locale-test.json",
      normalized: { true: "/ja-jp/locale-test/", false: "/ja-jp/locale-test" },
      matchedWithFlag: { true: "/ja-jp/locale-test", false: "/ja-jp/locale-test" },
    },
  ];

  for (const trailingSlash of [true, false]) {
    describe(`trailingSlash: ${trailingSlash}`, () => {
      const key = String(trailingSlash) as "true" | "false";

      describe("with the flag set", () => {
        const config: TrailingSlashConfig = {
          basePath: "",
          trailingSlash,
          skipMiddlewareUrlNormalize: true,
        };

        it.each(cases)("hands middleware $what as requested", ({ path }) => {
          expect(middlewarePathname(path, config, BUILD_ID)).toBe(path);
        });

        it.each(cases)(
          "still matches $what on the routed form",
          ({ path, normalized, matchedWithFlag }) => {
            expect(middlewareMatchPathname(path, config, BUILD_ID)).toBe(
              (matchedWithFlag ?? normalized)[key],
            );
          },
        );
      });

      describe("without the flag", () => {
        const config: TrailingSlashConfig = { basePath: "", trailingSlash };

        it.each(cases)("normalizes $what", ({ path, normalized }) => {
          expect(middlewarePathname(path, config, BUILD_ID)).toBe(normalized[key]);
        });
      });
    });
  }
});

describe("needsSlashNormalization / normalizeRepeatedSlashes", () => {
  it.each(["/a//b", "/a///b", "/basepath//en/x", "/a\\b", "/a/\\b"])(
    "flags %s as needing normalization",
    (path) => {
      expect(needsSlashNormalization(path)).toBe(true);
    },
  );

  it.each(["/a", "/a/b", "/basepath/en/x", "/"])(
    "leaves %s alone",
    (path) => {
      expect(needsSlashNormalization(path)).toBe(false);
    },
  );

  it.each([
    ["/a//b", "/a/b"],
    ["/a///b", "/a/b"],
    ["/basepath//en/x", "/basepath/en/x"],
    ["/a\\b", "/a/b"],
    ["/a//b?x=1//2", "/a/b?x=1//2"],
    ["/a//b?x=1//2?y=3", "/a/b?x=1//2?y=3"],
    ["/a//b/", "/a/b/"],
    ["//", "/"],
  ])("normalizes %s to %s", (input, expected) => {
    expect(normalizeRepeatedSlashes(input)).toBe(expected);
  });
});
