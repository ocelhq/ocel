import { describe, expect, it } from "bun:test";
import { findMostAtOnce } from "./tasks";

const span = (n: number, startedAt: number, finishedAt: number) => ({ n, startedAt, finishedAt });

describe("the most runs in progress at once", () => {
  it("is one for runs that follow each other, touching ends included", () => {
    expect(findMostAtOnce([span(1, 0, 10), span(2, 10, 20), span(3, 25, 30)])).toBe(1);
  });

  it("counts the runs that overlap, wherever they overlap", () => {
    expect(findMostAtOnce([span(1, 0, 10), span(2, 5, 15), span(3, 8, 9), span(4, 14, 20)])).toBe(
      3,
    );
  });

  it("is zero for no runs", () => {
    expect(findMostAtOnce([])).toBe(0);
  });
});
