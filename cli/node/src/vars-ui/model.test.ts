import { describe, expect, it } from "vitest";

import {
  coordinateLine,
  doneLabel,
  held,
  names,
  owedChildren,
  owedCount,
  tallyLine,
  treeOf,
  verdictLine,
  type Address,
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

const row = (key: string, cells: MatrixCell[], over: Partial<MatrixRow> = {}): MatrixRow => ({
  key,
  class: "plain",
  cells,
  ...over,
});

const stateOf = (
  rows: MatrixRow[],
  environments: string[] = [],
  columns = ["", "/web"],
): State => ({
  slug: "acme",
  tier: "production",
  other: "preview",
  environments,
  matrix: { columns, rows, apps: [] },
});

const at = (key: string, folder = "", environment = ""): Address => ({
  key,
  folder,
  environment,
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

describe("treeOf", () => {
  it("makes one row per declared key with the root cell as its value", () => {
    const tree = treeOf(
      stateOf([row("A", [cell({ set: true, version: 3 }), cell({ folder: "/web" })])]),
      [],
    );
    expect(tree).toHaveLength(1);
    expect(tree[0]?.root).toMatchObject({
      at: at("A"),
      kind: "root",
      set: true,
      version: 3,
      owed: false,
    });
    expect(tree[0]?.children).toEqual([]);
  });

  it("nests a folder cell only when it is set, required, faulty or overridden", () => {
    const tree = treeOf(
      stateOf(
        [
          row("A", [cell({}), cell({ folder: "/web" }), cell({ folder: "/api", set: true, version: 1 })]),
          row("B", [cell({ state: "forbidden" }), cell({ folder: "/web", state: "required" })], { scope: ["/web"] }),
          row("C", [cell({}), cell({ folder: "/web", problem: "too short", set: true, version: 2 })]),
          row("D", [cell({}), cell({ folder: "/web", overrides: [{ environment: "qa", version: 1 }] })]),
        ],
        ["qa"],
        ["", "/api", "/web"],
      ),
      [],
    );
    expect(tree.map((r) => r.children.map((c) => c.at))).toEqual([
      [at("A", "/api")],
      [at("B", "/web")],
      [at("C", "/web")],
      [at("D", "/web"), at("D", "/web", "qa")],
    ]);
    expect(tree[1]?.root.state).toBe("forbidden");
    expect(tree[1]?.children[0]).toMatchObject({ kind: "folder", owed: true });
    expect(tree[2]?.children[0]?.problem).toBe("too short");
    expect(tree[3]?.children[1]).toMatchObject({ kind: "environment", set: true, version: 1 });
  });

  it("lists root overrides before folder variants and flags orphans", () => {
    const tree = treeOf(
      stateOf(
        [
          row("A", [
            cell({ set: true, version: 5, overrides: [{ environment: "gone", version: 2, orphaned: true }] }),
            cell({ folder: "/web", set: true, version: 1 }),
          ]),
        ],
        ["staging"],
      ),
      [],
    );
    expect(tree[0]?.children.map((c) => [c.at.folder, c.at.environment, c.orphaned])).toEqual([
      ["", "gone", true],
      ["/web", "", false],
    ]);
  });

  it("materialises asked-for variants as empty extras and dedupes real ones", () => {
    const tree = treeOf(
      stateOf([row("A", [cell({}), cell({ folder: "/web", set: true, version: 1 })])], ["staging"]),
      [at("A", "/web"), at("A", "", "staging"), at("A", "/web", "staging")],
    );
    expect(tree[0]?.children.map((c) => [c.at.folder, c.at.environment, c.extra, c.set])).toEqual([
      ["", "staging", true, false],
      ["/web", "", false, true],
      ["/web", "staging", true, false],
    ]);
  });

  it("offers the folders and environments a key can still gain", () => {
    const tree = treeOf(
      stateOf(
        [
          row("A", [cell({}), cell({ folder: "/web", set: true, version: 1 }), cell({ folder: "/api" })]),
          row("B", [cell({ state: "forbidden" }), cell({ folder: "/web" }), cell({ folder: "/api", state: "forbidden" })], { scope: ["/web"] }),
        ],
        ["staging"],
        ["", "/web", "/api"],
      ),
      [at("A", "", "staging")],
    );
    expect(tree[0]?.options.map((o) => o.label)).toEqual([
      "/api",
      "staging in /web",
      "staging in /api",
    ]);
    expect(tree[1]?.options.map((o) => o.label)).toEqual(["/web", "staging in /web"]);
  });

  it("treats a reference as set", () => {
    const tree = treeOf(
      stateOf([row("A", [cell({ reference: { slug: "other", folder: "", key: "A" } })])]),
      [],
    );
    expect(tree[0]?.root).toMatchObject({ set: true, reference: { slug: "other" } });
  });

  it("counts owed children", () => {
    const tree = treeOf(
      stateOf(
        [row("B", [cell({ state: "forbidden" }), cell({ folder: "/web", state: "required" })], { scope: ["/web"] })],
      ),
      [],
    );
    expect(owedChildren(tree[0]!)).toBe(1);
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
