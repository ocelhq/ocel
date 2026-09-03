import { describe, expect, it } from "vitest";

import {
  addressable,
  coordinateLine,
  doneLabel,
  held,
  names,
  owedCount,
  socketState,
  tallyLine,
  verdictLine,
  type MatrixCell,
  type MatrixRow,
  type State,
} from "./model";

const cell = (over: Partial<MatrixCell>): MatrixCell => ({
  folder: "",
  state: "optional",
  set: false,
  version: 0,
  ...over,
});

const row = (key: string, cells: MatrixCell[]): MatrixRow => ({
  key,
  class: "plain",
  cells,
});

const stateOf = (rows: MatrixRow[], environments: string[] = []): State => ({
  slug: "acme",
  tier: "production",
  environments,
  matrix: { columns: ["", "/web"], rows, apps: [] },
});

describe("owedCount", () => {
  it("counts required empty cells and cells with a problem", () => {
    const current = stateOf([
      row("A", [cell({ state: "required" }), cell({ folder: "/web" })]),
      row("B", [cell({ set: true, version: 2, problem: "not a url" })]),
      row("C", [cell({ state: "required", set: true, version: 1 })]),
    ]);
    expect(owedCount(current)).toBe(2);
  });
});

describe("socketState", () => {
  it("ranks forbidden over faulty over held over owed", () => {
    expect(socketState(cell({ state: "forbidden", problem: "x" }))).toBe("forbidden");
    expect(socketState(cell({ set: true, problem: "x" }))).toBe("faulty");
    expect(socketState(cell({ set: true }))).toBe("held");
    expect(socketState(cell({ state: "required" }))).toBe("owed");
    expect(socketState(cell({}))).toBe("free");
  });
});

describe("held", () => {
  const base = cell({
    set: true,
    version: 4,
    overrides: [{ environment: "staging", version: 2 }],
  });

  it("reads the base cell for the empty environment", () => {
    expect(held(base, "")).toEqual({ set: true, version: 4 });
  });

  it("reads the override for a named environment", () => {
    expect(held(base, "staging")).toEqual({ set: true, version: 2 });
    expect(held(base, "qa")).toEqual({ set: false, version: 0 });
  });
});

describe("addressable", () => {
  it("lists the root, the named environments, then orphaned ones", () => {
    const current = stateOf([], ["staging"]);
    const withOrphan = cell({
      overrides: [{ environment: "gone", version: 1, orphaned: true }],
    });
    expect(addressable(current, withOrphan)).toEqual(["", "staging", "gone"]);
  });
});

describe("names", () => {
  it("joins two with and, more with commas", () => {
    expect(names(["a"])).toBe("a");
    expect(names(["a", "b"])).toBe("a and b");
    expect(names(["a", "b", "c"])).toBe("a, b and c");
  });

  it("folds the tail past five into a count", () => {
    expect(names(["a", "b", "c", "d", "e", "f", "g"])).toBe(
      "a, b, c, d, e and 2 others",
    );
  });
});

describe("coordinateLine", () => {
  const r = row("K", []);

  it("names the environment when one is addressed", () => {
    expect(coordinateLine(r, cell({ folder: "/web" }), "staging")).toBe(
      "/web · plain · read by staging alone",
    );
  });

  it("lists which environments do not read the base value", () => {
    expect(coordinateLine(r, cell({}), "")).toBe(
      "root · plain · all environments read it",
    );
    expect(
      coordinateLine(
        r,
        cell({ overrides: [{ environment: "staging", version: 1 }] }),
        "",
      ),
    ).toBe("root · plain · all environments except staging");
  });
});

describe("verdictLine", () => {
  it("prefers the orphan verdict over everything", () => {
    expect(verdictLine(cell({ set: true }), "gone", true, true)).toMatch(
      /gone no longer exists/,
    );
  });

  it("tells a required empty root apart from an optional one", () => {
    expect(verdictLine(cell({ state: "required" }), "", false, false)).toBe(
      "No value is set, and this cell is required.",
    );
    expect(verdictLine(cell({}), "", false, false)).toMatch(/overrides the root/);
  });
});

describe("labels", () => {
  it("pluralise the owed count", () => {
    expect(tallyLine(0)).toBe("every required cell is filled");
    expect(tallyLine(1)).toBe("1 cell to fill");
    expect(doneLabel(0)).toBe("Done — return to the terminal");
    expect(doneLabel(3)).toBe(
      "Return to the terminal with 3 cells still to fill",
    );
  });
});
