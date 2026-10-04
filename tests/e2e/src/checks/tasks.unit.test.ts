import { describe, expect, it } from "bun:test";
import { countStartedBeforeLast, findMostAtOnce } from "./tasks";

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

describe("the runs of one group that start before the last run of another", () => {
  it("is none when the other group all starts first", () => {
    const spans = [span(3, 0, 1), span(4, 1, 2), span(1, 2, 3), span(2, 3, 4)];
    expect(countStartedBeforeLast(spans, [1, 2], [3, 4])).toBe(0);
  });

  it("counts every run of the group that starts before the other's last, wherever it starts", () => {
    const spans = [span(1, 0, 1), span(3, 1, 2), span(2, 2, 3), span(4, 3, 4), span(5, 4, 5)];
    expect(countStartedBeforeLast(spans, [1, 2, 5], [3, 4])).toBe(2);
  });
});
