import { isrPrefixOf, tagNamespace, tagSnapshotKey } from "@framework/next-cache";
import contract from "@framework/next-cache/fixtures/edge-contract.json" with {
  type: "json",
};
import { afterEach, expect, test } from "vitest";
import { revalidatedHeader } from "../src/next/revalidation-signal.mjs";

afterEach(() => {
  for (const name of Object.values(contract.cacheStoreEnv)) delete process.env[name];
});

test("the snapshot is keyed at the suffix the deploy seeds it under", () => {
  const prefix = contract.tagNamespace.isrPrefix;
  expect(tagSnapshotKey(prefix)).toBe(prefix + contract.tagSnapshotSuffix);
});

test("tag records are namespaced exactly as the deploy grants them", () => {
  expect(tagNamespace(contract.tagNamespace.isrPrefix)).toBe(
    contract.tagNamespace.partitionKeyPrefix,
  );
});

test("the publisher reads back the very prefix the deploy wrote the tag items for", () => {
  expect(isrPrefixOf(contract.tagNamespace.partitionKeyPrefix)).toBe(
    contract.tagNamespace.isrPrefix,
  );
});

test("the contract's prefix and key are one coordinate, spelled twice", () => {
  const { project, env, app, release } = contract.tagNamespace.coordinate;
  expect(contract.tagNamespace.isrPrefix).toBe(`${env}/${project}/${app}/${release}/isr`);
  expect(contract.tagNamespace.partitionKeyPrefix).toBe(
    `PROJECT#${project}#STACK#${env}--${app}--${release}#TAG#`,
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

test("a revalidation is announced under the header name the edge reads", () => {
  expect(revalidatedHeader).toBe(contract.revalidatedHeader);
});
