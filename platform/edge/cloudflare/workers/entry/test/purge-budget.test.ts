import { describe, expect, it } from "vitest";
import { readPurgeBudget } from "../src/purge-budget";

describe("the purge budget the deploy writes", () => {
  it("reads the purges a minute and the tags a purge the edge states", () => {
    expect(readPurgeBudget('{"purgesPerMinute":5,"tagsPerPurge":100}')).toEqual({
      purgesPerMinute: 5,
      tagsPerPurge: 100,
    });
  });

  it.each([
    ["no budget", undefined],
    ["an empty budget", ""],
    ["a budget that is not JSON", "5/min"],
    ["a budget with no tags a purge", '{"purgesPerMinute":5}'],
    ["a budget of zero purges", '{"purgesPerMinute":0,"tagsPerPurge":100}'],
    ["a budget of a fraction of a tag", '{"purgesPerMinute":5,"tagsPerPurge":1.5}'],
  ])("refuses %s, so the queue never purges on a guess", (_name, declared) => {
    expect(() => readPurgeBudget(declared)).toThrow("OCEL_PURGE_BUDGET");
  });
});
