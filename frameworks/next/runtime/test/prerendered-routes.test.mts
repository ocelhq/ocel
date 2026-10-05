import { expect, test } from "vitest";
import { findPrerenderedRoute, prerenderedRoutes } from "../src/prerendered-routes.mjs";

function manifestOf(prerender: unknown, config: Record<string, unknown> = {}) {
  return { config, distDir: ".next", prerender };
}

const posts = {
  "/posts/[slug]": { routeRegex: "^/posts/([^/]+?)(?:/)?$", fallbackRevalidate: 30 },
};

test("finds a page prerendered with a revalidate window", () => {
  const routes = prerenderedRoutes(
    manifestOf({ routes: { "/blog": { initialRevalidateSeconds: 60 } } }),
  );

  expect(findPrerenderedRoute("/blog?page=2", routes)).toEqual({
    revalidate: 60,
    partiallyStatic: false,
    hasPrefetchData: false,
  });
});

test("finds a page prerendered to never revalidate by time", () => {
  const routes = prerenderedRoutes(
    manifestOf({ routes: { "/about": { initialRevalidateSeconds: false } } }),
  );

  expect(findPrerenderedRoute("/about", routes)?.revalidate).toBe(false);
});

test("finds a path of a dynamic route by its pattern", () => {
  const routes = prerenderedRoutes(manifestOf({ dynamicRoutes: posts }));

  expect(findPrerenderedRoute("/posts/hello", routes)).toEqual({
    revalidate: 30,
    partiallyStatic: false,
    hasPrefetchData: false,
  });
});

test("reads a partially static page and its static flight data from the manifest", () => {
  const routes = prerenderedRoutes(
    manifestOf({
      routes: {
        "/shop": { initialRevalidateSeconds: 60, renderingMode: "PARTIALLY_STATIC" },
        "/catalog": {
          initialRevalidateSeconds: 60,
          renderingMode: "PARTIALLY_STATIC",
          prefetchDataRoute: "/catalog.prefetch.rsc",
        },
      },
    }),
  );

  expect(findPrerenderedRoute("/shop", routes)).toEqual({
    revalidate: 60,
    partiallyStatic: true,
    hasPrefetchData: false,
  });
  expect(findPrerenderedRoute("/catalog", routes)?.hasPrefetchData).toBe(true);
});

test("finds nothing for a route Next did not prerender", () => {
  const routes = prerenderedRoutes(
    manifestOf({ routes: { "/blog": { initialRevalidateSeconds: 60 } }, dynamicRoutes: posts }),
  );

  expect(findPrerenderedRoute("/dashboard", routes)).toBeUndefined();
});

test("reads cache components from the app's config", () => {
  expect(prerenderedRoutes(manifestOf({}, { cacheComponents: true })).cacheComponents).toBe(true);
  expect(prerenderedRoutes(manifestOf({}, {})).cacheComponents).toBe(false);
  expect(prerenderedRoutes(null).cacheComponents).toBe(false);
});
