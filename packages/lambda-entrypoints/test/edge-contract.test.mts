import { tagNamespace, tagSnapshotKey } from "@framework/next-cache";
import contract from "@framework/next-cache/fixtures/edge-contract.json" with {
  type: "json",
};
import { afterEach, expect, test } from "vitest";

afterEach(() => {
  for (const name of Object.values(contract.cacheStoreEnv)) delete process.env[name];
});

test("the snapshot is keyed at the suffix the deploy seeds it under", () => {
  expect(tagSnapshotKey("prod/proj/web/BID")).toBe(
    "prod/proj/web/BID" + contract.tagSnapshotSuffix,
  );
});

test("tag records are namespaced exactly as the deploy grants them", () => {
  expect(tagNamespace(contract.tagNamespace.isrPrefix)).toBe(
    contract.tagNamespace.partitionKeyPrefix,
  );
});

test("the adopted store is named by exactly the variable the deploy injects", async () => {
  const env = contract.cacheStoreEnv;
  expect(Object.keys(env)).toEqual(["bucket"]);
  process.env[env.bucket] = "isr";

  const { entriesAdopted } = await import("../src/next/object-store.mjs");

  expect(entriesAdopted()).toBe(true);
});

test("no store is adopted when the contract's bucket name is unset", async () => {
  const { entriesAdopted } = await import("../src/next/object-store.mjs");

  expect(entriesAdopted()).toBe(false);
});
