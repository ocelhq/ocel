import { afterEach, describe, expect, it, vi } from "vitest";

import { CAPACITY, MEMO_TTL_MS, forget, memoize, memoized } from "../src/memo";

afterEach(() => {
  vi.useRealTimers();
});

describe("memo", () => {
  it("reads back what was memoized, until the entry lapses", () => {
    memoize("a", "hash-a", false);
    expect(memoized("a")).toMatchObject({ hash: "hash-a", refreshed: false });

    vi.useFakeTimers();
    vi.setSystemTime(Date.now() + MEMO_TTL_MS + 1);
    expect(memoized("a")).toBeUndefined();
  });

  it("forgets a prefix on demand", () => {
    memoize("b", "hash-b", false);
    forget("b");
    expect(memoized("b")).toBeUndefined();
  });

  // Nothing authenticates a caller before its prefix is looked up, so the set of
  // prefixes an isolate is asked about is attacker-chosen and unbounded.
  it("evicts oldest-first rather than growing without bound", () => {
    for (let i = 0; i < CAPACITY * 2; i++) memoize(`flood-${i}`, `hash-${i}`, false);

    expect(memoized("flood-0")).toBeUndefined();
    expect(memoized(`flood-${CAPACITY - 1}`)).toBeUndefined();
    expect(memoized(`flood-${CAPACITY}`)).toMatchObject({ hash: `hash-${CAPACITY}` });
    expect(memoized(`flood-${CAPACITY * 2 - 1}`)).toMatchObject({
      hash: `hash-${CAPACITY * 2 - 1}`,
    });
  });
});
