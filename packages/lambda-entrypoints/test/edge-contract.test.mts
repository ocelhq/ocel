import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

import { tagNamespace, tagSnapshotKey } from "@ocel/next-cache";
import { afterEach, expect, test } from "vitest";

const contract: {
  tagSnapshotSuffix: string;
  tagNamespace: { isrPrefix: string; partitionKeyPrefix: string };
  cacheStoreEnv: Record<"bucket", string>;
} = JSON.parse(
  readFileSync(
    fileURLToPath(
      new URL("../../next-cache/fixtures/edge-contract.json", import.meta.url),
    ),
    "utf8",
  ),
);

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
