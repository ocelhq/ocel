import { rmSync } from "node:fs";
import { join } from "node:path";
import { expect, onTestFinished, test } from "vitest";
import { readPrerender } from "../src/prerender-files.mjs";
import { writeServerDistFixture } from "../test-support/server-dist-fixture.mjs";

const staticRoute = { initialRevalidateSeconds: false, srcRoute: null, dataRoute: null };

function serverDist(...args: Parameters<typeof writeServerDistFixture>): string {
  const dist = writeServerDistFixture(...args);
  onTestFinished(dist.remove);
  return dist.serverDistDir;
}

test("reads the root page from the index files next build names it by", async () => {
  const dir = serverDist(
    { routes: { "/": staticRoute } },
    { "app/index.html": "<html>home</html>", "app/index.rsc": "RSC" },
  );

  const entry = await readPrerender(dir, "/index", { kind: "APP_PAGE" });

  expect(entry?.value).toMatchObject({ kind: "APP_PAGE", html: "<html>home</html>" });
  expect(entry?.cacheControl).toEqual({ revalidate: false });
});

test("reads a page under a route named index from below the index prefix next build adds", async () => {
  const dir = serverDist(
    { routes: { "/index/docs": staticRoute } },
    { "app/index/index/docs.html": "<html>docs</html>", "app/index/index/docs.rsc": "RSC" },
  );

  const entry = await readPrerender(dir, "/index/index/docs", { kind: "APP_PAGE" });

  expect(entry?.value.html).toBe("<html>docs</html>");
});

test("reads a pages-router page with the props next build wrote beside it", async () => {
  const dir = serverDist(
    { routes: { "/blog": { initialRevalidateSeconds: 60, srcRoute: "/blog" } } },
    { "pages/blog.html": "<html>blog</html>", "pages/blog.json": '{"pageProps":{"n":1}}' },
  );

  const entry = await readPrerender(dir, "/blog", { kind: "PAGES" });

  expect(entry?.value).toEqual({
    kind: "PAGES",
    html: "<html>blog</html>",
    pageData: { pageProps: { n: 1 } },
  });
  expect(entry?.cacheControl).toEqual({ revalidate: 60 });
});

test("reads a prerendered route handler's body with its status and headers", async () => {
  const dir = serverDist(
    { routes: { "/feed.xml": staticRoute } },
    {
      "app/feed.xml.body": "<rss/>",
      "app/feed.xml.meta": JSON.stringify({
        status: 200,
        headers: { "content-type": "application/xml" },
      }),
    },
  );

  const entry = await readPrerender(dir, "/feed.xml", { kind: "APP_ROUTE" });

  expect(entry?.value).toEqual({
    kind: "APP_ROUTE",
    body: Buffer.from("<rss/>").toString("base64"),
    status: 200,
    headers: { "content-type": "application/xml" },
  });
});

test("reads a partially prerendered page's postponed state and no flight data", async () => {
  const dir = serverDist(
    { routes: { "/shop": staticRoute } },
    {
      "app/shop.html": "<html>shell</html>",
      "app/shop.meta": JSON.stringify({ postponed: "resume-state" }),
    },
  );

  const entry = await readPrerender(dir, "/shop", { kind: "APP_PAGE", isRoutePPREnabled: true });

  expect(entry?.value.postponed).toBe("resume-state");
  expect(entry?.value.rscData).toBeUndefined();
});

test("reads a dynamic route's fallback page with the fallback's revalidate window", async () => {
  const dir = serverDist(
    {
      dynamicRoutes: {
        "/posts/[slug]": { fallback: "/posts/[slug].html", fallbackRevalidate: 30 },
      },
    },
    { "pages/posts/[slug].html": "<html>loading</html>" },
  );

  const entry = await readPrerender(dir, "/posts/[slug]", { kind: "PAGES", isFallback: true });

  expect(entry?.value).toEqual({ kind: "PAGES", html: "<html>loading</html>", pageData: {} });
  expect(entry?.cacheControl).toEqual({ revalidate: 30 });
});

test("reads nothing for a dynamic route that renders on demand", async () => {
  const dir = serverDist(
    { dynamicRoutes: { "/posts/[slug]": { fallback: null } } },
    { "pages/posts/[slug].html": "<html>stray</html>" },
  );

  expect(await readPrerender(dir, "/posts/[slug]", { kind: "PAGES" })).toBeNull();
});

test("reads nothing where next build wrote no prerender manifest", async () => {
  const dir = serverDist({ routes: { "/": staticRoute } }, { "app/index.html": "<html/>" });
  rmSync(join(dir, "..", "prerender-manifest.json"));

  expect(await readPrerender(dir, "/index", { kind: "APP_PAGE" })).toBeNull();
});

test("reads nothing for a fetch", async () => {
  const dir = serverDist({ routes: { "/": staticRoute } }, { "app/index.html": "<html/>" });

  expect(await readPrerender(dir, "/index", { kind: "FETCH" })).toBeNull();
});
