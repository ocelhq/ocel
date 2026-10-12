import { expect, test } from "vitest";
import { runTagWrites, tagWritesInFlight } from "../src/tag-records.mjs";

test("runs at most eight writes at a time, however many tags a request revalidates", async () => {
  let inFlight = 0;
  let most = 0;

  const failures = await runTagWrites(
    Array.from({ length: 200 }, (_, i) => `t${i}`),
    async () => {
      inFlight++;
      most = Math.max(most, inFlight);
      await new Promise((resolve) => setTimeout(resolve, 1));
      inFlight--;
    },
  );

  expect(failures).toEqual([]);
  expect(tagWritesInFlight).toBe(8);
  expect(most).toBe(8);
});

test("writes every tag it can, and hands back each failure once all have settled", async () => {
  const written: string[] = [];
  const down = new Error("throttled");

  const failures = await runTagWrites(["a", "b", "c"], async (tag) => {
    if (tag === "b") throw down;
    written.push(tag);
  });

  expect(written.sort()).toEqual(["a", "c"]);
  expect(failures).toEqual([down]);
});

test("writes nothing for no tags", async () => {
  expect(
    await runTagWrites([], async () => {
      throw new Error("unreachable");
    }),
  ).toEqual([]);
});
