import { newInstanceCache } from "@framework/next-runtime/instance-cache";
import { expect, test } from "vitest";
import { newFirestore } from "../src/next/firestore.mjs";
import { newInstanceUseCacheStore } from "../src/next/instance-stores.mjs";
import { newFirestoreTagRecords } from "../src/next/tag-records.mjs";
import { newGcpUseCacheStore } from "../src/next/use-cache-store.mjs";
import { newFirestoreDatabase } from "./firestore-database.mjs";

const database = "projects/p/databases/d";

function instance(fake: ReturnType<typeof newFirestoreDatabase>) {
  const tags = newFirestoreTagRecords(
    newFirestore({ database, fetch: fake.fetch }),
    "prod/shop/web/r1/isr",
  );
  return newGcpUseCacheStore(newInstanceUseCacheStore(newInstanceCache(1_000_000)), tags);
}

test("a tag one instance writes reaches the clock of another instance on its next read", async () => {
  const fake = newFirestoreDatabase({ database });
  const writer = instance(fake);
  const reader = instance(fake);

  expect(await writer.writeTag("posts", { stale: 1, expired: 5, writtenAt: 99 })).toBe(true);
  const read = await reader.readTagSnapshot(null);

  expect(read).toMatchObject({ status: "fresh", records: { posts: { stale: 1, expired: 5 } } });
});

test("use-cache entries stay in the instance until they move to Cloud Storage", async () => {
  const fake = newFirestoreDatabase({ database });
  const first = instance(fake);
  const second = instance(fake);
  const entry = { tags: [], stale: 1, timestamp: 2, expire: 3, revalidate: 4, body: "b" };

  await first.writeEntry("k", entry);

  expect(await first.readEntry("k")).toEqual(entry);
  expect(await second.readEntry("k")).toBeNull();
});
