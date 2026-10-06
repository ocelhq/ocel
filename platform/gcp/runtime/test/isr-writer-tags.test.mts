import type { TagRecord } from "@framework/next-cache";
import type { IsrWriterClient, TagSnapshotRead } from "@platform/edge-contract/isr-writer";
import { expect, it, vi } from "vitest";
import { newIsrWriterTagRecords } from "../src/next/isr-writer-tags.mjs";

function writer(overrides: Partial<IsrWriterClient> = {}): IsrWriterClient {
  return {
    readEntry: vi.fn(),
    writeEntry: vi.fn(),
    raiseTags: vi.fn(async () => {}),
    readTagSnapshot: vi.fn(async () => ({ kind: "absent" }) satisfies TagSnapshotRead),
    ...overrides,
  };
}

it("raises every tag published in one turn with one writer request", async () => {
  const client = writer();
  const tags = newIsrWriterTagRecords(client);

  await Promise.all([
    tags.publish("a", { stale: 5 }),
    tags.publish("b", { expired: 7 }),
    tags.publish("a", { stale: 9 }),
  ]);

  expect(client.raiseTags).toHaveBeenCalledTimes(1);
  expect(client.raiseTags).toHaveBeenCalledWith({ a: { stale: 9 }, b: { expired: 7 } });
});

it("resolves a published tag only once the writer answered", async () => {
  let answer!: () => void;
  const client = writer({
    raiseTags: vi.fn(() => new Promise<void>((resolve) => (answer = resolve))),
  });
  const tags = newIsrWriterTagRecords(client);
  let settled = false;

  const published = tags.publish("a", { stale: 5 }).then(() => {
    settled = true;
  });
  await new Promise((resolve) => setTimeout(resolve, 5));
  expect(settled).toBe(false);

  answer();
  await published;
  expect(settled).toBe(true);
});

it("rejects every tag of a raise the writer refused", async () => {
  const client = writer({
    raiseTags: vi.fn(async () => {
      throw new Error("raise: writer answered 500");
    }),
  });
  const tags = newIsrWriterTagRecords(client);

  const results = await Promise.allSettled([
    tags.publish("a", { stale: 5 }),
    tags.publish("b", { stale: 5 }),
  ]);

  expect(results.map((result) => result.status)).toEqual(["rejected", "rejected"]);
});

it("starts the next raise for a tag published while one is in flight", async () => {
  const answers: (() => void)[] = [];
  const client = writer({
    raiseTags: vi.fn(() => new Promise<void>((resolve) => answers.push(resolve))),
  });
  const tags = newIsrWriterTagRecords(client);

  const first = tags.publish("a", { stale: 1 });
  await new Promise((resolve) => setTimeout(resolve, 0));
  const second = tags.publish("b", { stale: 2 });
  await new Promise((resolve) => setTimeout(resolve, 0));

  expect(client.raiseTags).toHaveBeenCalledTimes(2);
  for (const answer of answers) answer();
  await Promise.all([first, second]);
});

it("reads the writer's snapshot as fresh records with its etag as the cursor", async () => {
  const records: Record<string, TagRecord> = { a: { stale: 5 } };
  const client = writer({
    readTagSnapshot: vi.fn(async () => ({
      kind: "fresh" as const,
      snapshot: { version: 1 as const, deployedAt: 1, generatedAt: 2, records },
      etag: '"e1"',
    })),
  });

  expect(await newIsrWriterTagRecords(client).read(null)).toEqual({
    status: "fresh",
    records,
    cursor: '"e1"',
  });
  expect(client.readTagSnapshot).toHaveBeenCalledWith(null);
});

it("reads an unchanged snapshot as unchanged and a missing one as no records", async () => {
  const unchanged = writer({
    readTagSnapshot: vi.fn(async () => ({ kind: "unchanged" as const })),
  });
  expect(await newIsrWriterTagRecords(unchanged).read('"e1"')).toEqual({ status: "unchanged" });

  expect(await newIsrWriterTagRecords(writer()).read(null)).toEqual({
    status: "fresh",
    records: {},
    cursor: null,
  });
});

it("reads a snapshot the writer could not answer as unusable", async () => {
  const client = writer({
    readTagSnapshot: vi.fn(async () => {
      throw new Error("status 500");
    }),
  });

  expect(await newIsrWriterTagRecords(client).read(null)).toEqual({ status: "unusable" });
});
