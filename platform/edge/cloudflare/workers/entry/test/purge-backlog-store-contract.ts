import { describe, expect, it } from "vitest";
import type { BacklogStore } from "../src/purge-backlog";

export function describeBacklogStore(
  name: string,
  withStore: (body: (store: BacklogStore) => void | Promise<void>) => Promise<void>,
): void {
  describe(`the ${name} backlog store`, () => {
    it("has nothing pending to begin with", () =>
      withStore((store) => {
        expect(store.hasAnyPending()).toBe(false);
        expect(store.readOldestRaisedAt()).toBeUndefined();
        expect(store.readBatch("purge", 10)).toEqual([]);
        expect(store.readFailures()).toBe(0);
        expect(store.readRetryAt()).toBeUndefined();
        expect(store.readPurgedEverythingAt()).toBeUndefined();
      }));

    it("keeps the oldest raisedAt and lets a purge win over an invalidate of one tag", () =>
      withStore((store) => {
        store.addPending("invalidate", "a", 500);
        store.addPending("purge", "a", 300);
        store.addPending("invalidate", "a", 900);

        expect(store.hasPending("purge")).toBe(true);
        expect(store.hasPending("invalidate")).toBe(false);
        expect(store.readOldestRaisedAt()).toBe(300);
      }));

    it("reads a batch oldest first, one mode, up to the limit", () =>
      withStore((store) => {
        store.addPending("purge", "c", 30);
        store.addPending("purge", "a", 10);
        store.addPending("invalidate", "z", 5);
        store.addPending("purge", "b", 20);

        expect(store.readBatch("purge", 2)).toEqual(["a", "b"]);
        expect(store.readBatch("invalidate", 5)).toEqual(["z"]);
      }));

    it("settles a batch: drops what was raised by then and records the cover", () =>
      withStore((store) => {
        store.addPending("purge", "a", 100);
        store.addPending("purge", "b", 100);
        store.addPending("purge", "b", 400);

        store.settleBatch("purge", ["a", "b"], 200);

        expect(store.readBatch("purge", 10)).toEqual(["b"]);
        expect(store.readCoveredAt("a", "purge")).toBe(200);
        expect(store.readCoveredAt("b", "purge")).toBe(200);
      }));

    it("keeps a pending purge when only an invalidate of its tag settles", () =>
      withStore((store) => {
        store.addPending("invalidate", "a", 100);
        store.addPending("purge", "a", 150);

        store.settleBatch("invalidate", ["a"], 200);

        expect(store.readBatch("purge", 10)).toEqual(["a"]);
      }));

    it("counts a purge as covering an invalidate but not the other way round", () =>
      withStore((store) => {
        store.settleBatch("purge", ["p"], 100);
        store.settleBatch("invalidate", ["i"], 100);

        expect(store.readCoveredAt("p", "invalidate")).toBe(100);
        expect(store.readCoveredAt("i", "invalidate")).toBe(100);
        expect(store.readCoveredAt("i", "purge")).toBeUndefined();
        expect(store.readCoveredAt("unseen", "purge")).toBeUndefined();
      }));

    it("never moves a recorded cover backwards", () =>
      withStore((store) => {
        store.settleBatch("purge", ["a"], 300);
        store.settleBatch("purge", ["a"], 200);

        expect(store.readCoveredAt("a", "purge")).toBe(300);
      }));

    it("settles everything: drops what was raised by then and remembers when", () =>
      withStore((store) => {
        store.addPending("purge", "old", 100);
        store.addPending("invalidate", "older", 50);
        store.addPending("purge", "late", 900);

        store.settleEverything(500);

        expect(store.readBatch("purge", 10)).toEqual(["late"]);
        expect(store.hasPending("invalidate")).toBe(false);
        expect(store.readPurgedEverythingAt()).toBe(500);
      }));

    it("remembers a backoff until it is cleared", () =>
      withStore((store) => {
        store.writeBackoff(3, 12_345);
        expect(store.readFailures()).toBe(3);
        expect(store.readRetryAt()).toBe(12_345);

        store.clearBackoff();

        expect(store.readFailures()).toBe(0);
        expect(store.readRetryAt()).toBeUndefined();
      }));

    it("forgets covers older than a time", () =>
      withStore((store) => {
        store.settleBatch("purge", ["old"], 100);
        store.settleBatch("invalidate", ["fresh"], 900);

        store.forgetCoveredBefore(500);

        expect(store.readCoveredAt("old", "purge")).toBeUndefined();
        expect(store.readCoveredAt("fresh", "invalidate")).toBe(900);
      }));
  });
}
