import { describe, expect, it } from "vitest";

import { DESTROY_PAGES, emptyPrefix } from "../src/prefix";

function fakeBucket(total: number) {
  let keys = Array.from({ length: total }, (_, at) => `p/isr/${at}`);
  const deleted: string[][] = [];
  const listed: { prefix?: string; cursor?: string }[] = [];
  const bucket = {
    async list(options: { prefix?: string; cursor?: string }) {
      listed.push(options);
      const page = keys.slice(0, 1000);
      return {
        objects: page.map((key) => ({ key })),
        truncated: keys.length > page.length,
        cursor: "more",
      };
    },
    async delete(batch: string[]) {
      deleted.push(batch);
      const gone = new Set(batch);
      keys = keys.filter((key) => !gone.has(key));
    },
  } as unknown as R2Bucket;
  return { bucket, deleted, listed };
}

describe("emptyPrefix", () => {
  it("lists under the prefix with a trailing slash, so a sibling release is never matched", async () => {
    const { bucket, listed } = fakeBucket(3);

    expect(await emptyPrefix(bucket, "p/isr")).toBe("emptied");

    expect(listed[0]?.prefix).toBe("p/isr/");
  });

  it("deletes every object in pages of one thousand", async () => {
    const { bucket, deleted } = fakeBucket(2_500);

    expect(await emptyPrefix(bucket, "p/isr")).toBe("emptied");

    expect(deleted.map((batch) => batch.length)).toEqual([1000, 1000, 500]);
  });

  it("calls an empty prefix emptied after one listing", async () => {
    const { bucket, deleted, listed } = fakeBucket(0);

    expect(await emptyPrefix(bucket, "p/isr")).toBe("emptied");
    expect(deleted).toEqual([]);
    expect(listed).toHaveLength(1);
  });

  it("answers more while objects remain past its page budget, and emptied once they are gone", async () => {
    const { bucket } = fakeBucket(1000 * DESTROY_PAGES + 10);

    expect(await emptyPrefix(bucket, "p/isr")).toBe("more");
    expect(await emptyPrefix(bucket, "p/isr")).toBe("emptied");
  });
});
