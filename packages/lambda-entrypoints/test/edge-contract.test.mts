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
  cacheStoreEnv: Record<
    "bucket" | "endpoint" | "region" | "accessKeyId" | "secretAccessKey",
    string
  >;
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

// The last binding that still reads the injected R2 credentials: the tag-clock
// snapshot publisher. Route entries stopped reading them when their reads joined
// their writes behind the ISR writer worker, and ocelhq-wvag.4 takes the
// publisher the same way — after which this contract item has no reader here.
test("the snapshot store is bound from exactly the names the membrane injects", async () => {
  const env = contract.cacheStoreEnv;
  process.env[env.bucket] = "isr";
  process.env[env.endpoint] = "https://acct.r2.cloudflarestorage.com";
  process.env[env.region] = "auto";
  process.env[env.accessKeyId] = "AK";
  process.env[env.secretAccessKey] = "s3cret";

  const { snapshotObjectStore } = await import("../src/next/object-store.mjs");
  const store = snapshotObjectStore()!;

  expect(store.bucket).toBe("isr");
  expect(await store.client.config.region()).toBe("auto");
  expect(await store.client.config.endpoint!()).toMatchObject({
    hostname: "acct.r2.cloudflarestorage.com",
  });
  expect(await store.client.config.credentials()).toMatchObject({
    accessKeyId: "AK",
    secretAccessKey: "s3cret",
  });
});

// The bucket alone decides adoption, so a rename of it does not degrade to the
// provider's own store quietly — it has to be the thing this asserts.
test("no store is adopted when the contract's bucket name is unset", async () => {
  const { snapshotObjectStore } = await import("../src/next/object-store.mjs");

  expect(snapshotObjectStore()).toBeNull();
});
