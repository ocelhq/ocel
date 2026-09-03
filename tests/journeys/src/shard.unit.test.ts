import { describe, expect, it } from "vitest";
import { parseShard } from "./shard";

describe("shard flag", () => {
  it("is absent when unset", () => {
    expect(parseShard(undefined)).toBeUndefined();
  });

  it("parses index and total", () => {
    expect(parseShard("2/3")).toEqual({ index: 2, total: 3 });
  });

  it("tolerates surrounding whitespace", () => {
    expect(parseShard(" 1/1 ")).toEqual({ index: 1, total: 1 });
  });

  it.each(["", "2", "2/", "a/b", "2/3/4", "-1/3", "1.5/3"])(
    "rejects %j",
    (value) => {
      expect(() => parseShard(value)).toThrow(/index\/total/);
    },
  );

  it("rejects an index past the total", () => {
    expect(() => parseShard("4/3")).toThrow(/between 1 and 3/);
  });

  it("rejects a zero index", () => {
    expect(() => parseShard("0/3")).toThrow(/between 1 and 3/);
  });

  it("rejects a zero total", () => {
    expect(() => parseShard("1/0")).toThrow(/at least 1/);
  });
});
