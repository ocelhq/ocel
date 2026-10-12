import { expect, it } from "vitest";

import fixture from "../fixtures/routes.json" with { type: "json" };
import {
  compileRoutes,
  findCacheRule,
  findHeaders,
  findRedirect,
  findRoute,
  findSlashRedirect,
  formatCacheControl,
  isStaticFirst,
  parseRoutes,
  readRoutes,
} from "../src/routes.mjs";

const compiled = compileRoutes(readRoutes(fixture.table));

it("finds the first route that matches each path after the base path, as the fixture says", () => {
  for (const { path, route, groups, cacheControl } of fixture.routes) {
    const found = findRoute(compiled, path);
    expect(found?.index ?? null, path).toBe(route);
    if (groups) expect(found?.groups, path).toEqual(groups);
    if (cacheControl) expect(formatCacheControl(found!.route.cache!), path).toBe(cacheControl);
  }
});

it("finds the cache rule of the route that matches first, and none for a route that caches nothing", () => {
  expect(findCacheRule(compiled, "/docs/blog/hello")).toEqual(fixture.table.routes[1]!.cache);
  expect(findCacheRule(compiled, "/docs/account")).toBeUndefined();
  expect(findCacheRule(compiled, "/docs/nowhere/at/all")).toBeUndefined();
  expect(findCacheRule(compiled, "/blog/hello")).toBeUndefined();
});

it("redirects as the fixture says, substituting the groups of the pattern", () => {
  for (const { path, status, location } of fixture.redirects) {
    const found = findRedirect(compiled, path);
    if (location === null) expect(found, path).toBeUndefined();
    else expect(found, path).toEqual({ status, location });
  }
});

it("applies every header rule that matches, in order", () => {
  for (const { path, headers } of fixture.headers) {
    expect(findHeaders(compiled, path), path).toEqual(headers);
  }
});

it("names the paths a prerendered file beats a function for", () => {
  for (const { path, matches } of fixture.staticFirst) {
    expect(isStaticFirst(compiled, path), path).toBe(matches);
  }
});

it("redirects a path to the form its trailing slash mode serves", () => {
  for (const { mode, basePath, path, redirectTo } of fixture.trailingSlash) {
    const table = compileRoutes(
      readRoutes({ ...fixture.table, trailingSlash: mode, basePath, redirects: [] }),
    );
    expect(findSlashRedirect(table, path) ?? null, `${mode} ${basePath} ${path}`).toBe(redirectTo);
  }
});

it("matches the patterns of the fixture with the groups the Go reader's engine gives them", () => {
  for (const { src, path, groups } of fixture.patterns) {
    const table = compileRoutes(
      readRoutes({
        ...fixture.table,
        basePath: "",
        routes: [{ src, function: "index" }],
      }),
    );
    const found = findRoute(table, path);
    if (groups === null) expect(found, `${src} ${path}`).toBeUndefined();
    else expect(found?.groups, `${src} ${path}`).toEqual(groups);
  }
});

it("refuses every invalid table of the fixture", () => {
  for (const { reason, table } of fixture.invalid) {
    expect(() => readRoutes(table), reason).toThrow(/route table/);
  }
});

it("refuses a table that is not JSON, and one that is not an object", () => {
  expect(() => parseRoutes("{")).toThrow(/route table/);
  expect(() => parseRoutes("[]")).toThrow(/route table/);
});

it("reads a table from its JSON text", () => {
  expect(parseRoutes(JSON.stringify(fixture.table))).toEqual(fixture.table);
});

it("matches no route for a path too long to match safely", () => {
  expect(findRoute(compiled, `/docs/blog/${"a".repeat(5000)}`)).toBeUndefined();
});

it("sends a redirect to the group it names as empty when the group took no part in the match", () => {
  const table = compileRoutes(
    readRoutes({
      ...fixture.table,
      basePath: "",
      redirects: [{ src: "/a(/b)?", location: "/c$1", status: 307 }],
    }),
  );
  expect(findRedirect(table, "/a")).toEqual({ status: 307, location: "/c" });
  expect(findRedirect(table, "/a/b")).toEqual({ status: 307, location: "/c/b" });
});
