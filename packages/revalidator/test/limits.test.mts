import { expect, it } from "vitest";

import {
  batchSize,
  functionTimeoutMs,
  originTimeoutMs,
  triggerTimeoutMs,
  visibilityTimeoutMs,
} from "../src/limits.mjs";

it("gives a full batch of unmemoized records room to finish inside the function timeout", () => {
  expect(batchSize * (triggerTimeoutMs + originTimeoutMs)).toBeLessThanOrEqual(functionTimeoutMs);
});

it("keeps the queue's visibility timeout longer than a function that runs to its own timeout", () => {
  expect(visibilityTimeoutMs).toBeGreaterThan(functionTimeoutMs);
});

it("documents the values .24 renders", () => {
  expect({ batchSize, triggerTimeoutMs, originTimeoutMs, functionTimeoutMs, visibilityTimeoutMs }).toEqual({
    batchSize: 10,
    triggerTimeoutMs: 10_000,
    originTimeoutMs: 2_000,
    functionTimeoutMs: 150_000,
    visibilityTimeoutMs: 300_000,
  });
});
