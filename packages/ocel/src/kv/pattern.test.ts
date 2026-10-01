import { describe, expect, it } from "vitest";
import { readKvFixture } from "../testing/kv-fixture.js";
import { buildKey, parsePattern } from "./pattern.js";

describe("the kv key fixture, which every SDK builds alike", () => {
  it.each(readKvFixture().keys)(
    "builds the key of $pattern as $key",
    ({ pattern, params, key }) => {
      expect(buildKey(parsePattern(pattern), params)).toBe(key);
    },
  );
});

describe("a key's parameter values", () => {
  const pattern = parsePattern("users/:id");

  it("takes a string or a safe integer, written in decimal", () => {
    expect(buildKey(pattern, { id: "ada" })).toBe("users/ada");
    expect(buildKey(pattern, { id: 42 })).toBe("users/42");
    expect(buildKey(pattern, { id: -7 })).toBe("users/-7");
    expect(buildKey(pattern, { id: Number.MAX_SAFE_INTEGER })).toBe("users/9007199254740991");
  });

  it.each([
    1.5,
    Number.NaN,
    Number.POSITIVE_INFINITY,
    Number.NEGATIVE_INFINITY,
    2 ** 53,
    -(2 ** 53),
  ])("refuses the number %d, which no other SDK takes as a key", (id) => {
    expect(() => buildKey(pattern, { id })).toThrow(/:id.*no safe integer/);
  });
});
