import { describe, expect, it } from "vitest";
import { resolveRoutes, type I18nConfig } from "@next/routing";

import { serve, type RouteDeps } from "../src/index";
import type { AssetBucket } from "../src/assets";
import { localeOf, resolveLocale } from "../src/i18n";

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

  it("redirects the root the build also names bare to the preferred locale", async () => {
    const deps = i18nDeps({ unlocalized: ["/", "/about"] });

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

  it("prefixes a path the build names both bare and under a locale", async () => {
    const deps = i18nDeps({
      unlocalized: ["/", "/about"],
      files: { "/en/about.html": "<h1>about</h1>" },
    });

    const res = await serve(new Request("https://app.example/about"), deps);

    expect(res.status).toBe(200);
    expect(res.headers.get("x-matched-path")).toBe("/en/about");
  });

  it("drops an optional catch-all placeholder no segment filled", async () => {
    const deps = i18nDeps();
    const searches: string[] = [];
    (deps.manifest.routes as { dynamicRoutes: unknown[] }).dynamicRoutes.push({
      sourceRegex: "^[/]?(?<nextLocale>[^/]{1,})(?:/(?<nxtPslug>.+?))?(?:/)?$",
      destination: "/$nextLocale/[[...slug]]?nxtPslug=$nxtPslug",
    });
    deps.manifest.pathnames = deps.manifest.pathnames.filter((p) => p !== "/fr");
    delete deps.manifest.dispatch["/fr"];
    deps.manifest.pathnames.push("/fr/[[...slug]]");
    deps.manifest.dispatch["/fr/[[...slug]]"] = { kind: "lambda", id: "posts" };
    deps.fetch = (async (input: Request) => {
      searches.push(new URL(input.url).search);
      return new Response("origin", { status: 200 });
    }) as unknown as typeof fetch;

    const res = await serve(new Request("https://app.example/fr"), deps);

    expect(res.status).toBe(200);
    expect(searches[0]).toBe("");
  });

  it("keeps an optional catch-all value a segment did fill", async () => {
    const deps = i18nDeps();
    const searches: string[] = [];
    (deps.manifest.routes as { dynamicRoutes: unknown[] }).dynamicRoutes.push({
      sourceRegex: "^[/]?(?<nextLocale>[^/]{1,})(?:/(?<nxtPslug>.+?))?(?:/)?$",
      destination: "/$nextLocale/[[...slug]]?nxtPslug=$nxtPslug",
    });
    deps.manifest.pathnames.push("/fr/[[...slug]]");
    deps.manifest.dispatch["/fr/[[...slug]]"] = { kind: "lambda", id: "posts" };
    deps.fetch = (async (input: Request) => {
      searches.push(new URL(input.url).search);
      return new Response("origin", { status: 200 });
    }) as unknown as typeof fetch;

    const res = await serve(new Request("https://app.example/fr/deep/page"), deps);

    expect(res.status).toBe(200);
    expect(searches[0]).toBe("?nxtPslug=deep%2Fpage");
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

  it("prefixes the locale the request's domain makes default", async () => {
    const deps = i18nDeps({
      i18n: { ...I18N, domains: [{ domain: "app.fr", defaultLocale: "fr" }] },
      files: { "/fr/about.html": "<h1>a propos</h1>" },
    });

    const res = await serve(new Request("https://app.fr/about"), deps);

    expect(res.status).toBe(200);
    expect(res.headers.get("x-matched-path")).toBe("/fr/about");
  });

  it("leaves a manifest without i18n untouched", async () => {
    const deps = i18nDeps({ i18n: undefined });

    const res = await serve(new Request("https://app.example/about"), deps);

    expect(res.status).toBe(404);
    expect(res.headers.has("x-matched-path")).toBe(false);
  });

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
