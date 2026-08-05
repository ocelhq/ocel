import { expect, it } from "vitest";

import {
  batchSize,
  functionTimeoutMs,
  originTimeoutMs,
  triggerTimeoutMs,
  visibilityTimeoutMs,
} from "../src/limits.mjs";

// These numbers are only correct in relation to each other, and the relations
// are what the README hands `.24` to render. A constant that drifts on its own
// produces a consumer that DLQs work it already did, which is invisible from
// anything else in this package.

it("gives a full batch of unmemoized records room to finish inside the function timeout", () => {
  expect(batchSize * (triggerTimeoutMs + originTimeoutMs)).toBeLessThanOrEqual(functionTimeoutMs);
});

it("keeps the queue's visibility timeout longer than a function that runs to its own timeout", () => {
  expect(visibilityTimeoutMs).toBeGreaterThan(functionTimeoutMs);
});

// The documented values, asserted by value: the README and `.24` quote these,
// and a silent change to one of them is a change to a rendered resource.
it("documents the values .24 renders", () => {
  expect({ batchSize, triggerTimeoutMs, originTimeoutMs, functionTimeoutMs, visibilityTimeoutMs }).toEqual({
    batchSize: 10,
    triggerTimeoutMs: 10_000,
    originTimeoutMs: 2_000,
    functionTimeoutMs: 150_000,
    visibilityTimeoutMs: 300_000,
  });
});
