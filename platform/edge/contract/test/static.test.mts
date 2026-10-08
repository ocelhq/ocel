import { expect, it } from "vitest";

import fixture from "../fixtures/static-rules.json" with { type: "json" };
import type { Static } from "../src/hosting.mjs";
import {
  cacheControlFor,
  IMMUTABLE_CACHE_CONTROL,
  REVALIDATE_CACHE_CONTROL,
} from "../src/static.mjs";

it("classifies every path as the fixture pkg/edge reads says", () => {
  for (const { path, immutable } of fixture.cases) {
    expect(cacheControlFor(fixture.static as Static, path), path).toBe(
      immutable ? IMMUTABLE_CACHE_CONTROL : REVALIDATE_CACHE_CONTROL,
    );
  }
});

it("revalidates every path of a build that states no static rules", () => {
  expect(cacheControlFor(undefined, "/_app/immutable/entry/start.js")).toBe(
    REVALIDATE_CACHE_CONTROL,
  );
});
