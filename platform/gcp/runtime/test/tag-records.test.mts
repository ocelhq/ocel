import { createHash } from "node:crypto";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { newFirestore } from "../src/next/firestore.mjs";
import { newFirestoreTagRecords } from "../src/next/tag-records.mjs";
import { newFirestoreDatabase } from "./firestore-database.mjs";

const database = "projects/p/databases/ocel-production-tags";
const isrPrefix = "prod/shop/web/r1a2b3c4d/isr";
const prefix = `${isrPrefix}/`;

beforeEach(() => {
  vi.useFakeTimers({ now: new Date("2026-01-01T00:00:00Z") });
});

afterEach(() => {
  vi.useRealTimers();
});

function open(options: { latencyMs?: number | ((commitIndex: number) => number) } = {}) {
  const fake = newFirestoreDatabase({ database, ...options });
  const firestore = newFirestore({ database, fetch: fake.fetch });
  return { fake, firestore, tags: newFirestoreTagRecords(firestore, isrPrefix) };
}

function documentName(tag: string, forPrefix = prefix): string {
  const id = createHash("sha256")
    .update(forPrefix + tag)
    .digest("hex");
  return `${database}/documents/tags/${id}`;
}

test("a published tag is written under a document named for the ISR prefix and the tag, with only the fields the record carries raised to their maximum", async () => {
  const { fake, tags } = open();

  await tags.publish("posts", { expired: 5000 });
  await tags.publish("cart", { stale: 1000, expired: 5000 });

  expect(fake.commits[0]?.writes).toEqual([
    {
      update: {
        name: documentName("posts"),
        fields: { prefix: { stringValue: prefix }, tag: { stringValue: "posts" } },
      },
      updateMask: { fieldPaths: ["prefix", "tag"] },
      updateTransforms: [
        { fieldPath: "expired", maximum: { integerValue: "5000" } },
        { fieldPath: "writtenAt", setToServerValue: "REQUEST_TIME" },
      ],
    },
  ]);
  expect(
    fake.commits[1]?.writes[0].updateTransforms.map((t: { fieldPath: string }) => t.fieldPath),
  ).toEqual(["stale", "expired", "writtenAt"]);
});

test("a later stale time another instance recorded is kept when an earlier one is published", async () => {
  const { fake, tags } = open();

  await tags.publish("posts", { stale: 9000 });
  await tags.publish("posts", { stale: 4000, expired: 2000 });

  const fields = fake.document(documentName("posts"));
  expect(fields?.stale).toEqual({ integerValue: "9000" });
  expect(fields?.expired).toEqual({ integerValue: "2000" });
});

test("tags published while a commit is in flight land together in the next commit", async () => {
  const { fake, tags } = open({ latencyMs: 50 });

  const first = tags.publish("a", { expired: 1 });
  await vi.advanceTimersByTimeAsync(10);
  const second = tags.publish("b", { expired: 2 });
  const third = tags.publish("c", { expired: 3 });
  await vi.advanceTimersByTimeAsync(200);
  await Promise.all([first, second, third]);

  expect(fake.commits.map((c) => c.writes.length)).toEqual([1, 2]);
});

test("the same tag published twice in one turn is one write holding the later times", async () => {
  const { fake, tags } = open();

  await Promise.all([tags.publish("a", { stale: 1 }), tags.publish("a", { expired: 7, stale: 3 })]);

  expect(fake.commits).toHaveLength(1);
  expect(fake.commits[0]?.writes).toHaveLength(1);
  expect(fake.document(documentName("a"))?.stale).toEqual({ integerValue: "3" });
  expect(fake.document(documentName("a"))?.expired).toEqual({ integerValue: "7" });
});

test("more tags than one commit may hold go in several commits in sequence", async () => {
  const { fake, tags } = open();

  await Promise.all(Array.from({ length: 501 }, (_, i) => tags.publish(`t${i}`, { expired: 1 })));

  expect(fake.commits.map((c) => c.writes.length)).toEqual([500, 1]);
});

test("each publish resolves only once the commit holding its tag has landed", async () => {
  const { fake, tags } = open({ latencyMs: 50 });
  let resolved = false;

  const publishing = tags.publish("a", { expired: 1 }).then(() => {
    resolved = true;
  });
  await vi.advanceTimersByTimeAsync(49);
  expect(resolved).toBe(false);
  await vi.advanceTimersByTimeAsync(1);
  await publishing;

  expect(resolved).toBe(true);
  expect(fake.documents.size).toBe(1);
});

test("a commit that keeps failing rejects every publish it held, naming the tags", async () => {
  const { fake, tags } = open();
  fake.fail(4, 503);

  const failures = [tags.publish("a", { expired: 1 }), tags.publish("b", { expired: 1 })].map((p) =>
    p.catch((error: Error) => error),
  );
  await vi.runAllTimersAsync();

  for (const failure of await Promise.all(failures)) {
    expect((failure as Error).message).toMatch(
      new RegExp(`^ocel: could not record tags a, b in ${database}: .*503`),
    );
  }
});

test("a commit refused for contention is retried until it lands", async () => {
  const { fake, tags } = open();
  fake.fail(8, 409);

  const publishing = tags.publish("hot", { expired: 1 });
  await vi.runAllTimersAsync();
  await publishing;

  expect(fake.documents.size).toBe(1);
});

test("a tag published while a contended commit waits to be retried lands without waiting for it", async () => {
  const { fake, tags } = open();
  fake.fail(4, 409);
  const landed: string[] = [];

  const first = tags.publish("a", { expired: 1 }).then(() => landed.push("a"));
  await vi.advanceTimersByTimeAsync(1);
  const second = tags.publish("b", { expired: 1 }).then(() => landed.push("b"));
  await vi.runAllTimersAsync();
  await Promise.all([first, second]);

  expect(landed).toEqual(["b", "a"]);
});

test("a commit that stays contended through every round rejects its publishes", async () => {
  const { fake, tags } = open();
  fake.fail(1000, 409);

  const failure = tags.publish("hot", { expired: 1 }).catch((error: Error) => error);
  await vi.runAllTimersAsync();

  expect(((await failure) as Error).message).toMatch(/could not record tags hot .*409/);
});

test("a tag the database stamped long before its commit landed is written again", async () => {
  const { fake, tags } = open({ latencyMs: (index) => (index === 0 ? 3000 : 10) });

  const publishing = tags.publish("a", { expired: 1 });
  await vi.advanceTimersByTimeAsync(4000);
  await publishing;

  expect(fake.commits).toHaveLength(2);
});

test("a tag that keeps landing too long after it was stamped is refused", async () => {
  const { tags } = open({ latencyMs: 3000 });

  const failure = tags.publish("a", { expired: 1 }).catch((error: Error) => error);
  await vi.advanceTimersByTimeAsync(20_000);

  expect(((await failure) as Error).message).toMatch(/tags a landed too long after/);
});

test("the first read returns every record of the ISR prefix and a cursor two seconds before the read", async () => {
  const { tags } = open();
  await tags.publish("posts", { stale: 10, expired: 20 });
  await tags.publish("cart", { expired: 30 });
  vi.setSystemTime(new Date("2026-01-01T00:01:00Z"));

  const read = await tags.read(null);

  expect(read).toEqual({
    status: "fresh",
    records: { posts: { stale: 10, expired: 20 }, cart: { expired: 30 } },
    cursor: "2026-01-01T00:00:58.000Z",
  });
});

test("a read after a cursor returns only records written since", async () => {
  const { tags } = open();
  await tags.publish("old", { expired: 1 });
  vi.setSystemTime(new Date("2026-01-01T00:00:10Z"));
  await tags.publish("new", { expired: 2 });

  const read = await tags.read("2026-01-01T00:00:05.000Z");

  expect(read).toMatchObject({ status: "fresh", records: { new: { expired: 2 } } });
  expect(Object.keys((read as { records: object }).records)).toEqual(["new"]);
});

test("a prefix nothing was revalidated under reads as fresh and empty, so the clock counts as synced", async () => {
  const { tags } = open();

  expect(await tags.read(null)).toEqual({
    status: "fresh",
    records: {},
    cursor: "2025-12-31T23:59:58.000Z",
  });
});

test("another ISR prefix's records are never read", async () => {
  const { fake, firestore, tags } = open();
  const other = newFirestoreTagRecords(firestore, "prod/shop/web/rOTHER/isr");
  await other.publish("posts", { expired: 1 });

  expect(fake.documents.size).toBe(1);
  expect((await tags.read(null)) as { records: object }).toMatchObject({ records: {} });
});
