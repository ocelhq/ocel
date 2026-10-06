import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { newFirestore } from "../src/next/firestore.mjs";
import { newFirestoreTagRecords } from "../src/next/tag-records.mjs";
import { newFirestoreDatabase } from "./firestore-database.mjs";
import { driveTagBurst, percentile, reportTimings } from "./tag-burst.mjs";

const database = "projects/p/databases/ocel-production-tags";
const isrPrefix = "prod/shop/web/r1a2b3c4d/isr";
const instanceCount = 20;
const tagsPerInstance = 25;
const commitLatencyMs = 50;

beforeEach(() => {
  vi.useFakeTimers({ now: new Date("2026-01-01T00:00:00Z") });
});

afterEach(() => {
  vi.useRealTimers();
});

test("twenty instances revalidating twenty-five tags each at once land every tag, and every other instance reads it within five seconds", async () => {
  const fake = newFirestoreDatabase({
    database,
    latencyMs: commitLatencyMs,
    limits: { prefixWritesPerSecond: 500, documentWritesPerSecond: 5 },
  });

  const burst = await driveTagBurst({
    newTags: () => newFirestoreTagRecords(newFirestore({ database, fetch: fake.fetch }), isrPrefix),
    wait: async (ms) => {
      await vi.advanceTimersByTimeAsync(ms);
    },
    durationMs: 15_000,
  });

  expect(burst.publishes).toBe(instanceCount * (tagsPerInstance + 1));
  expect(fake.documents.size).toBe(instanceCount * tagsPerInstance + 1);
  expect(burst.unseen).toEqual([]);

  const products = fake.document(
    [...fake.documents.keys()].find(
      (name) => fake.document(name)?.tag?.stringValue === "products",
    )!,
  );
  expect(products?.expired?.integerValue).toBe("1");

  reportTimings("publish to resolve", burst.resolveMs);
  reportTimings("publish to visible on another instance", burst.visibleElsewhereMs);

  expect(percentile(burst.resolveMs, 0.95)).toBeLessThan(2000);
  expect(percentile(burst.visibleElsewhereMs, 0.95)).toBeLessThan(5000);
});
