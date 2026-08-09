import { describe, expect, it } from "vitest";
import { resolveRoutes, type I18nConfig } from "@next/routing";

import { serve, type RouteDeps } from "../src/index";
import type { AssetBucket } from "../src/assets";
import { localeOf, resolveLocale } from "../src/i18n";

// Pages-router i18n: the build adapter emits one locale-prefixed pathname per
// page (/en/about, /fr/about) and a dynamic route whose sourceRegex carries a
// nextLocale group, exactly as Next's own adapter does. Nothing in the manifest
// answers the unprefixed path a browser asks for — routing has to prefix the
// request with the detected (or default) locale before matching, which is what
// upstream's resolve-routes does and what this worker forwards the i18n block
// for.

function assetStore(files: Record<string, string>): RouteDeps["assetStore"] {
  const store: AssetBucket = {
    async get(key) {
      const body = files[key];
      if (body === undefined) return null;
      return { body: new Blob([body]).stream() };
    },
  };
  return {
    store,
    assetPrefix: "",
    cache: { match: async () => undefined, put: async () => {} },
    waitUntil: () => {},
  };
}

const I18N = { locales: ["en", "fr"], defaultLocale: "en" };

function i18nDeps(
  over: {
    i18n?: RouteDeps["manifest"]["i18n"];
    basePath?: string;
    files?: Record<string, string>;
    // Outputs the build named without a locale, as an app-router page or a
    // public/ file is.
    unlocalized?: string[];
  } = {},
): RouteDeps & { forwarded: string[] } {
  const basePath = over.basePath ?? "";
  const i18n = "i18n" in over ? over.i18n : I18N;
  const forwarded: string[] = [];
  const page = (pathname: string) => `${basePath}${pathname}`;
  const unlocalized = over.unlocalized ?? [];
  return {
    manifest: {
      buildId: "b1",
      basePath,
      ...(i18n && { i18n }),
      pathnames: [
        // The root, as the adapter emits it: /en, never /en/.
        page("/en"),
        page("/fr"),
        page("/en/about"),
        page("/fr/about"),
        page("/en/posts/[slug]"),
        page("/fr/posts/[slug]"),
        "/api/hello",
        "/_next/static/x.js",
        ...unlocalized,
      ],
      routes: {
        beforeMiddleware: [],
        beforeFiles: [],
        afterFiles: [],
        dynamicRoutes: [
          {
            sourceRegex: `^${basePath}[/]?(?<nextLocale>[^/]{1,})/posts/(?<slug>.+?)(?:/)?$`,
            destination: `${basePath}/$nextLocale/posts/[slug]`,
          },
        ],
        onMatch: [],
        fallback: [],
      },
      dispatch: {
        [page("/en")]: { kind: "static" },
        [page("/fr")]: { kind: "static" },
        ...Object.fromEntries(unlocalized.map((p) => [p, { kind: "static" as const }])),
        [page("/en/about")]: { kind: "static" },
        [page("/fr/about")]: { kind: "static" },
        [page("/en/posts/[slug]")]: { kind: "lambda", id: "posts" },
        [page("/fr/posts/[slug]")]: { kind: "lambda", id: "posts" },
        "/api/hello": { kind: "lambda", id: "api" },
        "/_next/static/x.js": { kind: "static" },
      },
    },
    functionUrls: { posts: "https://fn.example.com", api: "https://fn.example.com" },
    slug: "p1",
    app: "web",
    assetStore: assetStore(over.files ?? {}),
    fetch: (async (input: Request) => {
      forwarded.push(new URL(input.url).pathname);
      return new Response("origin", { status: 200 });
    }) as unknown as typeof fetch,
    forwarded,
  };
}

describe("pages-router i18n", () => {
  it("serves the default locale's page for an unprefixed path", async () => {
    const deps = i18nDeps({ files: { "/en/about.html": "<h1>about</h1>" } });

    const res = await serve(new Request("https://app.example/about"), deps);

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("<h1>about</h1>");
    expect(res.headers.get("x-matched-path")).toBe("/en/about");
  });

  it("matches an unprefixed path against a locale-prefixed dynamic route", async () => {
    const deps = i18nDeps();

    const res = await serve(new Request("https://app.example/posts/a"), deps);

    expect(res.status).toBe(200);
    expect(res.headers.get("x-matched-path")).toBe("/en/posts/[slug]");
    expect(deps.forwarded).toEqual(["/en/posts/a"]);
  });

  it("leaves an already-prefixed path on its own locale", async () => {
    const deps = i18nDeps({ files: { "/fr/about.html": "<h1>a propos</h1>" } });

    const res = await serve(new Request("https://app.example/fr/about"), deps);

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("<h1>a propos</h1>");
    expect(res.headers.get("x-matched-path")).toBe("/fr/about");
  });

  // Next never localizes an API route: its adapter output carries no nextLocale
  // group, so a prefixed path would match nothing at all.
  it("does not prefix an API route", async () => {
    const deps = i18nDeps();

    const res = await serve(new Request("https://app.example/api/hello"), deps);

    expect(res.status).toBe(200);
    expect(res.headers.get("x-matched-path")).toBe("/api/hello");
    expect(deps.forwarded).toEqual(["/api/hello"]);
  });

  it("does not prefix a _next asset", async () => {
    const deps = i18nDeps({ files: { "/_next/static/x.js": "console.log(1)" } });

    const res = await serve(new Request("https://app.example/_next/static/x.js"), deps);

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("console.log(1)");
  });

  it("prefixes the locale after the basePath", async () => {
    const deps = i18nDeps({
      basePath: "/docs",
      files: { "/docs/en/about.html": "<h1>about</h1>" },
    });

    const res = await serve(new Request("https://app.example/docs/about"), deps);

    expect(res.status).toBe(200);
    expect(res.headers.get("x-matched-path")).toBe("/docs/en/about");
  });

  // The root is the one output the adapter names /en rather than /en/about —
  // and the one path whose prefixing has to drop the request's own slash, or
  // string equality misses everything the build emitted.
  it("serves the default locale's root for /", async () => {
    const deps = i18nDeps({ files: { "/en.html": "<h1>home</h1>" } });

    const res = await serve(new Request("https://app.example/"), deps);

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("<h1>home</h1>");
    expect(res.headers.get("x-matched-path")).toBe("/en");
  });

  it("serves the default locale's root under a basePath", async () => {
    const deps = i18nDeps({
      basePath: "/docs",
      files: { "/docs/en.html": "<h1>home</h1>" },
    });

    const res = await serve(new Request("https://app.example/docs"), deps);

    expect(res.status).toBe(200);
    expect(res.headers.get("x-matched-path")).toBe("/docs/en");
  });

  // Next's adapter localizes no app-router page, so a project carrying both an
  // app/ and an i18n config gets a manifest holding /en/about beside a bare
  // /dashboard. Prefixing that one would route it at a page no build emitted.
  it("leaves an app-router page in a hybrid build unprefixed", async () => {
    const deps = i18nDeps({
      unlocalized: ["/dashboard"],
      files: { "/dashboard.html": "<h1>dashboard</h1>" },
    });

    const res = await serve(new Request("https://app.example/dashboard"), deps);

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("<h1>dashboard</h1>");
    expect(res.headers.get("x-matched-path")).toBe("/dashboard");
  });

  it("leaves a public/ file unprefixed", async () => {
    const deps = i18nDeps({
      unlocalized: ["/next.svg"],
      files: { "/next.svg": "<svg/>" },
    });

    const res = await serve(new Request("https://app.example/next.svg"), deps);

    expect(res.status).toBe(200);
    expect(await res.text()).toBe("<svg/>");
  });

  // Next redirects on locale detection at the site root and nowhere else: the
  // default locale never appears in a URL a visitor is sent to.
  it("serves an unprefixed path under the default locale whatever Accept-Language says", async () => {
    const deps = i18nDeps({ files: { "/en/about.html": "<h1>about</h1>" } });

    const res = await serve(
      new Request("https://app.example/about", {
        headers: { "accept-language": "fr" },
        redirect: "manual",
      }),
      deps,
    );

    expect(res.status).toBe(200);
    expect(res.headers.get("x-matched-path")).toBe("/en/about");
  });

  it("redirects the root to the locale the NEXT_LOCALE cookie names", async () => {
    const deps = i18nDeps();

    const res = await serve(
      new Request("https://app.example/", {
        headers: { cookie: "NEXT_LOCALE=fr" },
        redirect: "manual",
      }),
      deps,
    );

    expect(res.status).toBe(307);
    expect(new URL(res.headers.get("location") ?? "").pathname).toBe("/fr");
  });

  it("redirects the root to the locale Accept-Language prefers", async () => {
    const deps = i18nDeps();

    const res = await serve(
      new Request("https://app.example/", {
        headers: { "accept-language": "fr-FR,fr;q=0.9" },
        redirect: "manual",
      }),
      deps,
    );

    expect(res.status).toBe(307);
    expect(new URL(res.headers.get("location") ?? "").pathname).toBe("/fr");
  });

  it("does not redirect the root towards the default locale", async () => {
    const deps = i18nDeps({ files: { "/en.html": "<h1>home</h1>" } });

    const res = await serve(
      new Request("https://app.example/", {
        headers: { "accept-language": "en-US,en;q=0.9" },
        redirect: "manual",
      }),
      deps,
    );

    expect(res.status).toBe(200);
    expect(res.headers.get("x-matched-path")).toBe("/en");
  });

  it("serves the default locale with localeDetection off, whatever the cookie says", async () => {
    const deps = i18nDeps({
      i18n: { ...I18N, localeDetection: false },
      files: { "/en.html": "<h1>home</h1>" },
    });

    const res = await serve(
      new Request("https://app.example/", {
        headers: { cookie: "NEXT_LOCALE=fr" },
        redirect: "manual",
      }),
      deps,
    );

    expect(res.status).toBe(200);
    expect(res.headers.get("x-matched-path")).toBe("/en");
  });

  it("routes a single-locale config through the same prefixing", async () => {
    const deps = i18nDeps({
      i18n: { locales: ["en"], defaultLocale: "en" },
      files: { "/en/about.html": "<h1>about</h1>" },
    });

    const res = await serve(new Request("https://app.example/about"), deps);

    expect(res.headers.get("x-matched-path")).toBe("/en/about");
  });

  // A domain's own default locale wins over the config's, and a request already
  // on that domain is served rather than redirected across it.
  it("prefixes the locale the request's domain makes default", async () => {
    const deps = i18nDeps({
      i18n: { ...I18N, domains: [{ domain: "app.fr", defaultLocale: "fr" }] },
      files: { "/fr/about.html": "<h1>a propos</h1>" },
    });

    const res = await serve(new Request("https://app.fr/about"), deps);

    expect(res.status).toBe(200);
    expect(res.headers.get("x-matched-path")).toBe("/fr/about");
  });

  // The app-router path: no i18n block in the manifest, no normalization.
  it("leaves a manifest without i18n untouched", async () => {
    const deps = i18nDeps({ i18n: undefined });

    const res = await serve(new Request("https://app.example/about"), deps);

    expect(res.status).toBe(404);
    expect(res.headers.has("x-matched-path")).toBe(false);
  });

  // A pages-router data URL already names its locale (Next emits it even for the
  // default), so normalization must leave it alone rather than prefix it again.
  it("leaves a _next/data URL's own locale in place", async () => {
    const deps = i18nDeps();
    deps.manifest.pathnames.push("/_next/data/b1/en/about.json");
    deps.manifest.dispatch["/_next/data/b1/en/about.json"] = {
      kind: "lambda",
      id: "posts",
    };
    (deps.manifest.routes as { shouldNormalizeNextData?: boolean }).shouldNormalizeNextData =
      true;

    const res = await serve(
      new Request("https://app.example/_next/data/b1/en/about.json"),
      deps,
    );

    expect(res.status).toBe(200);
    expect(deps.forwarded).toEqual(["/_next/data/b1/en/about.json"]);
  });

  it("answers an unmatched path with the locale's own 404 document", async () => {
    const deps = i18nDeps({ files: { "/en/404.html": "<h1>en 404</h1>" } });

    const res = await serve(new Request("https://app.example/nope/nope"), deps);

    expect(res.status).toBe(404);
    expect(await res.text()).toBe("<h1>en 404</h1>");
  });

  it("answers a prefixed unmatched path with that locale's 404 document", async () => {
    const deps = i18nDeps({
      files: { "/en/404.html": "<h1>en 404</h1>", "/fr/404.html": "<h1>fr 404</h1>" },
    });

    const res = await serve(new Request("https://app.example/fr/nope/nope"), deps);

    expect(res.status).toBe(404);
    expect(await res.text()).toBe("<h1>fr 404</h1>");
  });
});

describe("resolveLocale", () => {
  const at = (pathname: string, basePath = "", headers = new Headers()) =>
    resolveLocale(I18N, basePath, [], new URL(`https://app.example${pathname}`), headers);

  // A basePath owns its own segment and nothing that merely starts with its
  // letters: /documents is not a page under /docs.
  it("only strips a basePath on a segment boundary", () => {
    expect(at("/documents", "/docs").pathname).toBe("/en/documents");
    expect(at("/docs/about", "/docs").pathname).toBe("/docs/en/about");
    expect(at("/docs", "/docs").pathname).toBe("/docs/en");
  });

  it("keeps the locale a path already names", () => {
    expect(at("/fr/about")).toEqual({ pathname: "/fr/about" });
  });

  it("prefixes the root without a trailing slash", () => {
    expect(at("/").pathname).toBe("/en");
  });
});

// The worker prefixes the locale itself and hands resolveRoutes `i18n:
// undefined` so the library never does it too (src/i18n). Only a comment says
// so, so this pins both halves: given an i18n block the library prefixes on its
// own, and given none — the way the worker calls it — it must leave the
// already-normalized path alone. An upgrade that resumed prefixing regardless
// would double it to /en/en/about, and this is what fails first.
describe("@next/routing boundary", () => {
  const call = (i18n: I18nConfig | undefined, pathname: string) =>
    resolveRoutes({
      url: new URL(`https://app.example${pathname}`),
      buildId: "b1",
      basePath: "",
      i18n,
      headers: new Headers(),
      requestBody: undefined as unknown as ReadableStream,
      pathnames: ["/en/about"],
      routes: {
        beforeMiddleware: [],
        beforeFiles: [],
        afterFiles: [],
        dynamicRoutes: [],
        onMatch: [],
        fallback: [],
      } as unknown as Parameters<typeof resolveRoutes>[0]["routes"],
      invokeMiddleware: async () => ({}),
    });

  it("prefixes the locale only when handed an i18n block, which the worker never does", async () => {
    expect((await call(I18N, "/about")).resolvedPathname).toBe("/en/about");
    expect((await call(undefined, "/en/about")).resolvedPathname).toBe("/en/about");
  });
});

describe("localeOf", () => {
  const at = (pathname: string, basePath = "") =>
    localeOf(I18N, basePath, new URL(`https://app.example${pathname}`));

  it("reads the locale a path names, past its basePath", () => {
    expect(at("/docs/fr/about", "/docs")).toBe("fr");
    expect(at("/docs/about", "/docs")).toBe("en");
    expect(at("/documents", "/docs")).toBe("en");
  });
});
