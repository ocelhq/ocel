import { expect, it } from "vitest";

import fixture from "../fixtures/cache-rules.json" with { type: "json" };
import { findCacheRule, formatCacheControl, readCacheRules } from "../src/cache-rules.mjs";

it("finds the first rule that matches each path, as the fixture says", () => {
  for (const { path, rule, cacheControl } of fixture.cases) {
    const found = findCacheRule(fixture.rules, path);
    expect(found, path).toEqual(fixture.rules[rule]);
    expect(formatCacheControl(found!), path).toBe(cacheControl);
  }
});

it("finds no rule in a build that states none", () => {
  expect(findCacheRule(undefined, "/blog/post")).toBeUndefined();
  expect(findCacheRule([], "/blog/post")).toBeUndefined();
});

it("reads the rules pkg/edge hands an origin as one JSON variable", () => {
  expect(readCacheRules(JSON.stringify(fixture.rules))).toEqual(fixture.rules);
});

it("reads no rules from a variable that is unset or empty", () => {
  expect(readCacheRules(undefined)).toEqual([]);
  expect(readCacheRules("")).toEqual([]);
});

it("refuses the variable when it is not a list of rules", () => {
  expect(() => readCacheRules("{")).toThrow(/OCEL_CACHE_RULES/);
  expect(() => readCacheRules("{}")).toThrow(/OCEL_CACHE_RULES/);
});

it("refuses every invalid rule of the fixture", () => {
  for (const { reason, rule } of fixture.invalid) {
    expect(() => readCacheRules(JSON.stringify([rule])), reason).toThrow(/OCEL_CACHE_RULES/);
  }
});
