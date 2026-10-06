import { createHash, randomBytes } from "node:crypto";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { newFirestore } from "../../src/next/firestore.mjs";
import { newFirestoreTagRecords } from "../../src/next/tag-records.mjs";
import { driveTagBurst, percentile, reportTimings } from "../tag-burst.mjs";
import { type Backend, openBackend } from "./backend.mjs";

const maxDeletesPerCommit = 500;

describe("a burst of tag publishes against a live backend", () => {
  let backend: Backend;
  let database: string;
  const isrPrefix = `live/${randomBytes(6).toString("hex")}/isr`;
  let tags: string[] = [];

  beforeAll(async () => {
    backend = await openBackend();
    database = backend.tagDatabase();
  });

  afterAll(async () => {
    if (backend === undefined) return;
    if (database !== undefined) {
      const firestore = newFirestore({
        database,
        endpoint: backend.firestoreEndpoint,
        metadataOrigin: backend.metadataOrigin,
      });
      const names = tags.map((tag) => {
        const id = createHash("sha256").update(`${isrPrefix}/${tag}`).digest("hex");
        return `${database}/documents/tags/${id}`;
      });
      for (let from = 0; from < names.length; from += maxDeletesPerCommit) {
        await firestore.commit(
          names.slice(from, from + maxDeletesPerCommit).map((name) => ({ delete: name })),
        );
      }
    }
    await backend.close();
  });

  it("lands every tag of twenty instances revalidating twenty-five tags each, and every other instance reads it within five seconds", async () => {
    const burst = await driveTagBurst({
      newTags: () =>
        newFirestoreTagRecords(
          newFirestore({
            database,
            endpoint: backend.firestoreEndpoint,
            metadataOrigin: backend.metadataOrigin,
          }),
          isrPrefix,
        ),
      wait: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
      durationMs: 15_000,
    });
    tags = burst.tags;

    reportTimings("publish to resolve", burst.resolveMs);
    reportTimings("publish to visible on another instance", burst.visibleElsewhereMs);

    expect(burst.publishes).toBe(20 * 26);
    expect(burst.unseen).toEqual([]);
    expect(percentile(burst.resolveMs, 0.95)).toBeLessThan(2000);
    expect(percentile(burst.visibleElsewhereMs, 0.95)).toBeLessThan(5000);
  });
});
