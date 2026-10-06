import { expect, test } from "vitest";
import { readRefreshTask } from "../src/next/refresh-task.mjs";

const refresh = {
  url: "/blog?page=2",
  key: "blog",
  lastModified: 1_000,
  headers: { host: "shop.example", "x-ocel-refresh": "1000" },
};

const body = (override: Record<string, unknown> = {}, inner: Record<string, unknown> = {}) =>
  JSON.stringify({
    isrPrefix: "prod/shop/web/r1/isr",
    refresh: { ...refresh, ...inner },
    ...override,
  });

test("a task body names the deployment and the refresh it carries", () => {
  expect(readRefreshTask(body())).toEqual({ isrPrefix: "prod/shop/web/r1/isr", refresh });
});

test("a refresh url that leaves the instance is not a task", () => {
  for (const url of ["http://evil/", "//evil/x", "/\\evil/x", "/a\nb"]) {
    expect(readRefreshTask(body({}, { url }))).toBeUndefined();
  }
});

test("a refresh of an entry the store cannot address is not a task", () => {
  for (const key of ["", "/blog", "../x", "a\\b"]) {
    expect(readRefreshTask(body({}, { key }))).toBeUndefined();
  }
});

test("a body that is not a refresh is not a task", () => {
  expect(readRefreshTask("{")).toBeUndefined();
  expect(readRefreshTask("null")).toBeUndefined();
  expect(readRefreshTask(body({ isrPrefix: undefined }))).toBeUndefined();
  expect(readRefreshTask(body({ isrPrefix: "" }))).toBeUndefined();
  expect(readRefreshTask(body({}, { lastModified: "1" }))).toBeUndefined();
  expect(readRefreshTask(body({}, { headers: { host: 1 } }))).toBeUndefined();
});

test("a task carries no field beyond the ones it names", () => {
  const task = readRefreshTask(body({ extra: 1 }, { more: 2 }));

  expect(task).toEqual({ isrPrefix: "prod/shop/web/r1/isr", refresh });
});
