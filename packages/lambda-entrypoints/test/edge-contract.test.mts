import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

import { tagNamespace, tagSnapshotKey } from "@ocel/next-cache";
import { afterEach, expect, test } from "vitest";

// The names and keys that cross the Go/TS boundary. Each side declares its own
// spelling — the deploy and the membrane in Go, the readers here — and nothing
// in either build compares them, so a rename on one side is silent: the store is
// simply never adopted, or the snapshot is written where nobody looks. This
// fixture is what turns that into a failing test on both sides.
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

// The one contract item with no reader on this tier: the Lambda is handed the
// finished namespace in its env, so only the deploy and the edge derive it, and
// this is the whole of what stops those two spellings from parting company.
test("tag records are namespaced exactly as the deploy grants them", () => {
  expect(tagNamespace(contract.tagNamespace.isrPrefix)).toBe(
    contract.tagNamespace.partitionKeyPrefix,
  );
});

// The whole of what the deploy still injects about the adopted store, and the
// whole of what this tier reads: one bucket name, and no credential. Entries
// travel through the ISR writer worker in both directions and the tag clock is
// published off the state table's stream, so a deployed function has nothing
// left to sign an R2 request with.
test("the adopted store is named by exactly the variable the deploy injects", async () => {
  const env = contract.cacheStoreEnv;
  expect(Object.keys(env)).toEqual(["bucket"]);
  process.env[env.bucket] = "isr";

  const { entriesAdopted } = await import("../src/next/object-store.mjs");

  expect(entriesAdopted()).toBe(true);
});

// The bucket alone decides adoption, so a rename of it does not degrade to the
// provider's own store quietly — it has to be the thing this asserts.
test("no store is adopted when the contract's bucket name is unset", async () => {
  const { entriesAdopted } = await import("../src/next/object-store.mjs");

  expect(entriesAdopted()).toBe(false);
});
