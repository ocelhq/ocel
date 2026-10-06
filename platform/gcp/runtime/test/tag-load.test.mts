import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { newFirestore } from "../src/next/firestore.mjs";
import { newFirestoreTagRecords } from "../src/next/tag-records.mjs";
import { newFirestoreDatabase } from "./firestore-database.mjs";

const database = "projects/p/databases/ocel-production-tags";
const isrPrefix = "prod/shop/web/r1a2b3c4d/isr";
const instanceCount = 20;
const tagsPerInstance = 25;
const readEveryMs = 2000;
const publishSpreadMs = 1000;
const commitLatencyMs = 50;

beforeEach(() => {
  vi.useFakeTimers({ now: new Date("2026-01-01T00:00:00Z") });
});

afterEach(() => {
  vi.useRealTimers();
});

function percentile(values: number[], p: number): number {
  const sorted = [...values].sort((a, b) => a - b);
  return sorted[Math.min(sorted.length - 1, Math.ceil(p * sorted.length) - 1)]!;
}

test("twenty instances revalidating twenty-five tags each at once land every tag, and every other instance reads it within five seconds", async () => {
  const fake = newFirestoreDatabase({
    database,
    latencyMs: commitLatencyMs,
    limits: { prefixWritesPerSecond: 500, documentWritesPerSecond: 5 },
  });
  const started = Date.now();
  const instances = Array.from({ length: instanceCount }, (_, id) => ({
    id,
    tags: newFirestoreTagRecords(newFirestore({ database, fetch: fake.fetch }), isrPrefix),
    cursor: null as string | null,
    seenAt: new Map<string, number>(),
  }));

  const publishedAt = new Map<string, number>();
  const resolveMs: number[] = [];
  const publishes: Promise<unknown>[] = [];
  const publish = (instance: (typeof instances)[number], tag: string) => {
    const at = Date.now() - started;
    const key = `${instance.id}:${tag}`;
    publishedAt.set(key, at);
    publishes.push(
      instance.tags
        .publish(tag, { expired: at + 1 })
        .then(() => resolveMs.push(Date.now() - started - at)),
    );
  };

  for (const instance of instances) {
    publish(instance, "products");
    for (let j = 0; j < tagsPerInstance; j++) {
      setTimeout(
        () => publish(instance, `i${instance.id}-t${j}`),
        Math.floor((j * publishSpreadMs) / tagsPerInstance),
      );
    }
    const phase = (instance.id * readEveryMs) / instanceCount;
    setTimeout(function tick() {
      void instance.tags.read(instance.cursor).then((read) => {
        if (read.status !== "fresh") return;
        instance.cursor = read.cursor;
        for (const tag of Object.keys(read.records)) {
          if (!instance.seenAt.has(tag)) instance.seenAt.set(tag, Date.now() - started);
        }
      });
      setTimeout(tick, readEveryMs);
    }, phase);
  }

  await vi.advanceTimersByTimeAsync(15_000);
  await Promise.all(publishes);

  expect(publishes).toHaveLength(instanceCount * (tagsPerInstance + 1));
  expect(fake.documents.size).toBe(instanceCount * tagsPerInstance + 1);

  const visibleElsewhereMs: number[] = [];
  for (const instance of instances) {
    for (const other of instances) {
      if (other === instance) continue;
      for (const tag of Array.from(
        { length: tagsPerInstance },
        (_, j) => `i${instance.id}-t${j}`,
      )) {
        const seen = other.seenAt.get(tag);
        expect(seen, `${other.id} never read ${tag}`).toBeDefined();
        visibleElsewhereMs.push(seen! - publishedAt.get(`${instance.id}:${tag}`)!);
      }
    }
  }
  const products = fake.document(
    [...fake.documents.keys()].find(
      (name) => fake.document(name)?.tag?.stringValue === "products",
    )!,
  );
  expect(products?.expired?.integerValue).toBe("1");

  const report = (label: string, values: number[]) =>
    console.log(
      `${label}: p50=${percentile(values, 0.5)}ms p95=${percentile(values, 0.95)}ms max=${Math.max(...values)}ms`,
    );
  report("publish to resolve", resolveMs);
  report("publish to visible on another instance", visibleElsewhereMs);

  expect(percentile(resolveMs, 0.95)).toBeLessThan(2000);
  expect(percentile(visibleElsewhereMs, 0.95)).toBeLessThan(5000);
});
