import { describe, expect, it } from "vitest";
import { readPurgeBudget } from "../src/purge-budget";

describe("the purge budget the deploy writes", () => {
  it("reads the tags a purge the edge states", () => {
    expect(readPurgeBudget('{"tagsPerPurge":100}')).toEqual({ tagsPerPurge: 100 });
  });

  it.each([
    ["no budget", undefined],
    ["an empty budget", ""],
    ["a budget that is not JSON", "5/min"],
    ["a budget with no tags a purge", "{}"],
    ["a budget of zero tags a purge", '{"tagsPerPurge":0}'],
    ["a budget of a fraction of a tag", '{"tagsPerPurge":1.5}'],
  ])("refuses %s, so the queue never purges on a guess", (_name, declared) => {
    expect(() => readPurgeBudget(declared)).toThrow("OCEL_PURGE_BUDGET");
  });
});
