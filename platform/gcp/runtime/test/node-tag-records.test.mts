import type { TagRecord } from "@platform/edge-contract/purge";
import { expect, test } from "vitest";

import { openPurgeFor } from "../src/cache/purge-target.mjs";
import { newTagRecordWriter } from "../src/node/tag-records.mjs";

test("writes nothing where the service has no edge to clear", () => {
  expect(newTagRecordWriter(undefined)).toBeUndefined();
});

test("hands the edge a purge as an expired tag and an invalidation as a stale one", async () => {
  const handed: ReadonlyMap<string, TagRecord>[] = [];
  const write = newTagRecordWriter(async (tags) => {
    handed.push(tags);
  })!;

  await write([
    { tag: "posts", mode: "purge", raisedAt: 700 },
    { tag: "path:/blog/a", mode: "invalidate", raisedAt: 800 },
  ]);

  expect(handed).toHaveLength(1);
  expect([...handed[0]!]).toEqual([
    ["posts", { expired: 700 }],
    ["path:/blog/a", { stale: 800 }],
  ]);
});

test("lets the failure of the edge reach the caller", async () => {
  const write = newTagRecordWriter(async () => {
    throw new Error("Cloud CDN refused");
  })!;

  await expect(write([{ tag: "posts", mode: "purge", raisedAt: 1 }])).rejects.toThrow(
    /Cloud CDN refused/,
  );
});

test("finds an edge to clear where the service is told a url map and a release, or a purge endpoint", () => {
  const release = "shop/prod/web/r1a2b3c4d";

  expect(openPurgeFor({})).toBeUndefined();
  expect(
    openPurgeFor({
      OCEL_CDN_URL_MAP: "projects/acme/global/urlMaps/routes",
      OCEL_CACHE_TAG_PURGE: "1",
      OCEL_STORAGE_COORDINATE: release,
    }),
  ).toBeTypeOf("function");
  expect(
    openPurgeFor({
      OCEL_PURGE_URL: "https://shop.acct.workers.dev/.ocel/purge",
      OCEL_PURGE_SECRET: "secret",
      OCEL_CACHE_TAG_PURGE: "1",
      OCEL_STORAGE_COORDINATE: release,
    }),
  ).toBeTypeOf("function");
});

test("refuses a url map or purge endpoint with no release to clear tags of", () => {
  expect(() => openPurgeFor({ OCEL_CDN_URL_MAP: "projects/acme/global/urlMaps/routes" })).toThrow(
    /OCEL_CDN_URL_MAP/,
  );
  expect(() =>
    openPurgeFor({ OCEL_PURGE_URL: "https://x/.ocel/purge", OCEL_PURGE_SECRET: "s" }),
  ).toThrow(/OCEL_PURGE_URL/);
});
