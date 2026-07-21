import { tagSnapshotKey, type TagSnapshot } from "@ocel/next-cache";
import { describe, expect, it } from "vitest";

import { createTagClock } from "../src/tag-clock";

const cfg = { isrPrefix: "prod/proj/app/build" };
const snapshotKey = tagSnapshotKey(cfg.isrPrefix);

const snapshot = (over: Partial<TagSnapshot> = {}): TagSnapshot => ({
  version: 1,
  deployedAt: 500,
  generatedAt: 900,
  validUntil: 300_900,
  records: {},
  ...over,
});

function storeWith(body: string | null, opts: { fail?: boolean } = {}) {
  const gets: string[] = [];
  return {
    gets,
    async get(key: string) {
      gets.push(key);
      if (opts.fail) throw new Error("store down");
      if (body === null) return null;
      return { etag: `"${key}"`, text: async () => body };
    },
  };
}

it("reports a tag expired after the entry was written as expired", async () => {
  const snap = snapshot({ records: { posts: { expired: 2_000 } } });
  const clock = createTagClock(cfg, { store: storeWith(JSON.stringify(snap)) });
  // entry written at 1_000, tag expired at 2_000, now 3_000 => expired.
  expect(await clock.expired(["posts"], 1_000, 3_000)).toBe(true);
});

it("reports no expiry when the tag lapsed before the entry was written", async () => {
  const snap = snapshot({ records: { posts: { expired: 500 } } });
  const clock = createTagClock(cfg, { store: storeWith(JSON.stringify(snap)) });
  expect(await clock.expired(["posts"], 1_000, 3_000)).toBe(false);
});

it("returns 'untrusted' on a missing snapshot", async () => {
  const clock = createTagClock(cfg, { store: storeWith(null) });
  expect(await clock.expired(["posts"], 1_000, 3_000)).toBe("untrusted");
});

it("returns 'untrusted' past the snapshot's trust window", async () => {
  const snap = snapshot({ validUntil: 2_000, records: { posts: { expired: 2_500 } } });
  const clock = createTagClock(cfg, { store: storeWith(JSON.stringify(snap)) });
  expect(await clock.expired(["posts"], 1_000, 3_000)).toBe("untrusted");
});

it("returns 'untrusted' on a store error", async () => {
  const clock = createTagClock(cfg, { store: storeWith(null, { fail: true }) });
  expect(await clock.expired(["posts"], 1_000, 3_000)).toBe("untrusted");
});

it("reads the snapshot object under the build prefix", async () => {
  const snap = snapshot();
  const store = storeWith(JSON.stringify(snap));
  const clock = createTagClock(cfg, { store });
  await clock.expired(["posts"], 1_000, 3_000);
  expect(store.gets).toContain(snapshotKey);
});
