import { expect, test } from "vitest";
import { notePurge, purgedSince, purgeHeaderFor, purgeMark } from "../src/purge-signal.mjs";

test("reports an expired tag raised after the mark as a purge", () => {
  const mark = purgeMark();

  notePurge("cart", { expired: 500 }, 1_000);

  expect(purgedSince(mark)).toEqual([{ tag: "cart", mode: "purge", raisedAt: 500 }]);
});

test("reports a tag only marked stale as an invalidate", () => {
  const mark = purgeMark();

  notePurge("home", { stale: 700 }, 1_000);

  expect(purgedSince(mark)).toEqual([{ tag: "home", mode: "invalidate", raisedAt: 700 }]);
});

test("reports a tag that expires later as an invalidate", () => {
  const mark = purgeMark();

  notePurge("home", { stale: 700, expired: 90_000 }, 1_000);

  expect(purgedSince(mark)).toEqual([{ tag: "home", mode: "invalidate", raisedAt: 700 }]);
});

test("reports nothing for a record with neither time", () => {
  const mark = purgeMark();

  notePurge("home", {}, 1_000);

  expect(purgedSince(mark)).toEqual([]);
});

test("reports only what was raised after the mark", () => {
  notePurge("before", { expired: 1 }, 1_000);
  const mark = purgeMark();

  notePurge("after", { expired: 2 }, 1_000);

  expect(purgedSince(mark).map(({ tag }) => tag)).toEqual(["after"]);
});

test("keeps the newest raises when more arrive than it remembers", () => {
  const mark = purgeMark();

  for (let i = 0; i < 2_500; i++) notePurge(`t${i}`, { expired: 1 }, 1_000);

  const reported = purgedSince(mark).map(({ tag }) => tag);
  expect(reported.length).toBeLessThanOrEqual(2_000);
  expect(reported.at(-1)).toBe("t2499");
});

test("writes the header with each tag stamped by the release token", () => {
  const entries = [
    { tag: "products", mode: "invalidate" as const, raisedAt: 1 },
    { tag: "_N_T_/blog/a", mode: "purge" as const, raisedAt: 2 },
  ];

  expect(purgeHeaderFor("r0a1b2c3d", entries)).toBe(
    "invalidate r0a1b2c3d|products purge r0a1b2c3d|_N_T_/blog/a",
  );
});

test("writes no header for no entries", () => {
  expect(purgeHeaderFor("r0a1b2c3d", [])).toBeUndefined();
});

test("leaves out a tag the edge cannot store, and writes the rest", () => {
  const entries = [
    { tag: "résumé", mode: "purge" as const, raisedAt: 1 },
    { tag: "cart", mode: "purge" as const, raisedAt: 1 },
  ];

  expect(purgeHeaderFor("r0a1b2c3d", entries)).toBe("purge r0a1b2c3d|cart");
});
