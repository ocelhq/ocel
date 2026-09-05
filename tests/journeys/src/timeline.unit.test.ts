import { describe, expect, it } from "bun:test";
import { legOf, timelineOf, type TimelineInput, timingTable } from "./timeline";

const START = 1_000_000;

function at(offsetS: number, lengthS: number) {
  return { startTime: START + offsetS * 1000, duration: lengthS * 1000 };
}

const overlapping: TimelineInput = {
  runStart: START,
  runEnd: START + 200_000,
  workers: 2,
  prepareMs: 12_000,
  tests: [
    { cell: "with-sst", leg: "up", title: "up", ...at(0, 60) },
    { cell: "with-sst", leg: "contract", title: "health", ...at(60, 10) },
    { cell: "with-sst", leg: "destroy", title: "destroy", ...at(70, 110) },
    { cell: "next-hello", leg: "up", title: "up", ...at(0, 30) },
    { cell: "next-hello", leg: "contract", title: "health", ...at(30, 5) },
    { cell: "next-hello", title: "publish · a bucket", ...at(35, 5) },
    { cell: "next-hello", leg: "destroy", title: "destroy", ...at(40, 20) },
  ],
  modules: [
    { cell: "with-sst", duration: 180_000 },
    { cell: "next-hello", duration: 60_000 },
  ],
};

describe("legOf", () => {
  it("takes the planned leg when the plan named one", () => {
    expect(legOf({ cell: "a", leg: "redeploy", title: "health", startTime: 0, duration: 1 })).toBe(
      "redeploy",
    );
  });

  it("folds an unplanned row into the leg its title prefixes", () => {
    expect(legOf({ cell: "a", title: "rollback · health", startTime: 0, duration: 1 })).toBe(
      "rollback",
    );
  });

  it("folds a ladder phase into other", () => {
    expect(legOf({ cell: "a", title: "publish · a bucket", startTime: 0, duration: 1 })).toBe(
      "other",
    );
  });
});

describe("timelineOf", () => {
  const timeline = timelineOf(overlapping);

  it("counts the wall, the file total and the speed-up", () => {
    expect(timeline.wall).toBe(200);
    expect(timeline.files).toBe(240);
    expect(timeline.speedUp).toBe(1.2);
  });

  it("sees both cells running at once", () => {
    expect(timeline.maxOverlap).toBe(2);
  });

  it("names the cell that runs the tail alone", () => {
    expect(timeline.tail).toEqual({ cell: "with-sst", seconds: 120 });
  });

  it("keeps the prepare it was handed", () => {
    expect(timeline.prepare).toBe(12);
  });

  it("orders the cells by file wall and folds every leg", () => {
    expect(timeline.cells.map((row) => row.cell)).toEqual(["with-sst", "next-hello"]);
    expect(timeline.cells[1]).toEqual({
      cell: "next-hello",
      start: 0,
      legs: { up: 30, contract: 5, other: 5, destroy: 20 },
      file: 60,
    });
  });
});

describe("timingTable", () => {
  it("renders the stat line and a row per cell", () => {
    expect(
      timingTable(timelineOf(overlapping), { target: "aws", runId: "42" }),
    ).toBe(
      [
        "### timing · aws · run 42",
        "",
        "wall 200s · Σ files 240s · 2 workers · speed-up 1.2x · max overlap 2 · prepare 12s · tail: with-sst alone for 120s",
        "",
        "| cell | start | up | contract | redeploy | rollback | destroy | other | file |",
        "| --- | --- | --- | --- | --- | --- | --- | --- | --- |",
        "| with-sst | 0 | 60 | 10 | 0 | 0 | 110 | 0 | 180 |",
        "| next-hello | 0 | 30 | 5 | 0 | 0 | 20 | 5 | 60 |",
        "",
      ].join("\n"),
    );
  });

  it("says the lane had no solo tail", () => {
    const table = timingTable(
      timelineOf({
        runStart: START,
        runEnd: START + 10_000,
        workers: 4,
        tests: [
          { cell: "a", leg: "up", title: "up", ...at(0, 10) },
          { cell: "b", leg: "up", title: "up", ...at(0, 10) },
        ],
        modules: [],
      }),
      { target: "vps", runId: "7" },
    );
    expect(table).toContain("### timing · vps · run 7");
    expect(table).toContain("tail: none");
    expect(table).not.toContain("prepare");
  });
});
