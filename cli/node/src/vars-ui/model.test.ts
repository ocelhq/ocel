import { describe, expect, it } from "vitest";

import {
  addressKey,
  applyDotenv,
  dirtyEntries,
  doneLabel,
  held,
  isDirty,
  names,
  owedChildren,
  owedCount,
  planCopy,
  readersOf,
  reduceSave,
  revealable,
  saveSummary,
  sizeLine,
  tallyLine,
  treeOf,
  variantsOf,
  type Address,
  type SaveResult,
  type MatrixCell,
  type MatrixRow,
  type OtherValue,
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

  it("carries the class down so a secret is never revealable", () => {
    const tree = treeOf(
      stateOf([
        row("S", [cell({ set: true, version: 1 }), cell({ folder: "/web", set: true, version: 1 })], { class: "secret" }),
        row("P", [cell({ set: true, version: 1 }), cell({ folder: "/web" })]),
      ]),
      [],
    );
    expect(variantsOf(tree).map((v) => [v.at.key, v.at.folder, v.class, revealable(v)])).toEqual([
      ["S", "", "secret", false],
      ["S", "/web", "secret", false],
      ["P", "", "plain", true],
    ]);
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

describe("readersOf", () => {
  const apps = [
    { name: "web", folder: "/web" },
    { name: "api", folder: "/api" },
    { name: "root-app", folder: "" },
  ];

  it("names every app for an unscoped key, since each reads the root", () => {
    const tree = treeOf(stateOf([row("A", [cell({}), cell({ folder: "/web" })])]), []);
    expect(readersOf(tree[0]!, apps).map((a) => a.name)).toEqual(["web", "api", "root-app"]);
  });

  it("names only the apps bound to a scoped key's folders", () => {
    const tree = treeOf(
      stateOf(
        [row("B", [cell({ state: "forbidden" }), cell({ folder: "/web" }), cell({ folder: "/api", state: "forbidden" })], { scope: ["/web"] })],
        [],
        ["", "/web", "/api"],
      ),
      [],
    );
    expect(readersOf(tree[0]!, apps).map((a) => a.name)).toEqual(["web"]);
  });
});

describe("sizeLine", () => {
  it("keeps bytes under a kibibyte and rounds above", () => {
    expect(sizeLine(48)).toBe("48 B");
    expect(sizeLine(2048)).toBe("2.0 KiB");
  });
});

describe("applyDotenv", () => {
  const rows = treeOf(
    stateOf(
      [
        row("A", [cell({ set: true, version: 1 }), cell({ folder: "/web" })]),
        row("S", [cell({}), cell({ folder: "/web" })], { class: "secret" }),
        row("R", [cell({ reference: { slug: "billing", folder: "", key: "R" } }), cell({ folder: "/web" })]),
        row("W", [cell({ state: "forbidden" }), cell({ folder: "/web", state: "required" })], { scope: ["/web"] }),
      ],
      [],
    ),
    [],
  );
  const entries = [
    { key: "A", value: "a" },
    { key: "S", value: "s" },
    { key: "R", value: "r" },
    { key: "W", value: "w" },
    { key: "NOPE", value: "x" },
  ];

  it("fills root rows, overwrites secrets, skips references and reports the rest", () => {
    const out = applyDotenv(rows, entries, "");
    expect(out.fills).toEqual([
      { at: at("A"), value: "a", materialise: false },
      { at: at("S"), value: "s", materialise: false },
    ]);
    expect(out.undeclared).toEqual(["NOPE"]);
    expect(out.skipped.map((s) => s.key)).toEqual(["R", "W"]);
    expect(out.skipped[0]?.reason).toMatch(/reads billing.*ocel env ref/);
    expect(out.skipped[1]?.reason).toBe("nothing reads W in root");
  });

  it("scopes to a folder and asks to materialise cells the tree does not show", () => {
    const out = applyDotenv(rows, entries, "/web");
    expect(out.fills).toEqual([
      { at: at("A", "/web"), value: "a", materialise: true },
      { at: at("S", "/web"), value: "s", materialise: true },
      { at: at("R", "/web"), value: "r", materialise: true },
      { at: at("W", "/web"), value: "w", materialise: false },
    ]);
    expect(out.skipped).toEqual([]);
  });
});

describe("planCopy", () => {
  const rows = treeOf(
    stateOf(
      [
        row("A", [cell({ set: true, version: 4 }), cell({ folder: "/web" })]),
        row("S", [cell({}), cell({ folder: "/web" })], { class: "secret" }),
        row("R", [cell({ reference: { slug: "billing", folder: "", key: "R" } }), cell({ folder: "/web" })]),
        row("W", [cell({ state: "forbidden" }), cell({ folder: "/web", state: "required" })], { scope: ["/web"] }),
      ],
      ["staging"],
    ),
    [],
  );
  const other = (over: Partial<OtherValue> & { key: string }): OtherValue => ({
    folder: "",
    environment: "",
    version: 1,
    class: "plain",
    ...over,
  });

  it("fills empty cells, marks set ones as overwrites, and keeps versions read here", () => {
    const plan = planCopy(rows, ["staging"], [
      other({ key: "A", value: "a-there" }),
      other({ key: "A", folder: "/web", value: "a-web" }),
      other({ key: "S", class: "secret" }),
      other({ key: "A", environment: "staging", value: "a-staging" }),
    ]);
    expect(plan.overwrites).toEqual([
      { at: at("A"), class: "plain", there: "a-there", hereSet: true, hereVersion: 4, materialise: false },
    ]);
    expect(plan.fills).toEqual([
      { at: at("A", "/web"), class: "plain", there: "a-web", hereSet: false, hereVersion: 0, materialise: true },
      { at: at("S"), class: "secret", there: undefined, hereSet: false, hereVersion: 0, materialise: false },
      { at: at("A", "", "staging"), class: "plain", there: "a-staging", hereSet: false, hereVersion: 0, materialise: true },
    ]);
  });

  it("lists unreadable cells rather than dropping them", () => {
    const plan = planCopy(rows, [], [other({ key: "A", error: "timed out" })]);
    expect(plan.unreadable).toEqual([{ at: at("A"), error: "timed out" }]);
    expect(plan.fills).toEqual([]);
    expect(plan.overwrites).toEqual([]);
  });

  it("skips what cannot land here and says why", () => {
    const plan = planCopy(rows, [], [
      other({ key: "NOPE", value: "x" }),
      other({ key: "W", value: "x" }),
      other({ key: "A", environment: "gone", value: "x" }),
      other({ key: "R", value: "x" }),
    ]);
    expect(plan.skipped.map((s) => s.reason)).toEqual([
      "NOPE is not declared here",
      "nothing reads W in root here",
      "no environment named gone exists here",
      "R in root reads billing here; a copy would break the link",
    ]);
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

describe("dirtyEntries", () => {
  const rows = treeOf(
    stateOf(
      [
        row("A", [cell({ set: true, version: 3 }), cell({ folder: "/web", set: true, version: 1 })]),
        row("B", [cell({ state: "required" })]),
      ],
      ["staging"],
    ),
    [at("A", "", "staging")],
  );
  const key = (a: Address) => addressKey(a);

  it("is empty when no draft differs from its baseline", () => {
    expect(dirtyEntries(rows, new Map(), new Map())).toEqual([]);
    expect(
      dirtyEntries(rows, new Map([[key(at("A")), "x"]]), new Map([[key(at("A")), "x"]])),
    ).toEqual([]);
  });

  it("carries the version each dirty row was read at, in table order", () => {
    const drafts = new Map([
      [key(at("B")), "new"],
      [key(at("A", "/web")), "web"],
      [key(at("A", "", "staging")), "stage"],
      [key(at("A")), ""],
    ]);
    expect(dirtyEntries(rows, drafts, new Map())).toEqual([
      { at: at("A", "", "staging"), value: "stage", version: 0 },
      { at: at("A", "/web"), value: "web", version: 1 },
      { at: at("B"), value: "new", version: 0 },
    ]);
  });

  it("treats clearing a revealed value as a change to empty", () => {
    const baselines = new Map([[key(at("A")), "old"]]);
    const drafts = new Map([[key(at("A")), ""]]);
    expect(dirtyEntries(rows, drafts, baselines)).toEqual([
      { at: at("A"), value: "", version: 3 },
    ]);
    expect(isDirty(at("A"), drafts, baselines)).toBe(true);
  });
});

describe("reduceSave", () => {
  const drafts = new Map([
    [addressKey(at("A")), "a"],
    [addressKey(at("B")), "b"],
    [addressKey(at("C")), "c"],
    [addressKey(at("D")), "d"],
  ]);
  const results: SaveResult[] = [
    { at: at("A"), ok: true },
    { at: at("B"), ok: false, status: 409, message: "moved" },
    { at: at("C"), ok: false, status: 502, message: "store down" },
  ];

  it("clears saved drafts and keeps the rest, marking why", () => {
    const out = reduceSave(drafts, new Map(), new Map(), results);
    expect([...out.drafts.keys()]).toEqual([
      addressKey(at("B")),
      addressKey(at("C")),
      addressKey(at("D")),
    ]);
    expect(out.problems.get(addressKey(at("B")))).toEqual({ kind: "conflict", message: "moved" });
    expect(out.problems.get(addressKey(at("C")))).toEqual({ kind: "error", message: "store down" });
    expect([out.saved, out.conflicted, out.failed]).toEqual([1, 1, 1]);
  });

  it("drops an old problem once its row saves", () => {
    const stale = new Map([[addressKey(at("A")), { kind: "conflict" as const, message: "was" }]]);
    const out = reduceSave(drafts, new Map(), stale, [{ at: at("A"), ok: true }]);
    expect(out.problems.size).toBe(0);
  });

  it("moves a saved draft into its baseline when the row was revealed", () => {
    const revealed = new Map([[addressKey(at("A")), "old"]]);
    const out = reduceSave(drafts, revealed, new Map(), [
      { at: at("A"), ok: true },
      { at: at("B"), ok: true },
    ]);
    expect(out.baselines.get(addressKey(at("A")))).toBe("a");
    expect(out.baselines.has(addressKey(at("B")))).toBe(false);
  });

  it("summarises the batch honestly", () => {
    expect(saveSummary(reduceSave(drafts, new Map(), new Map(), [{ at: at("A"), ok: true }]))).toBe(
      "Saved 1 change.",
    );
    expect(saveSummary(reduceSave(drafts, new Map(), new Map(), results))).toBe(
      "Saved 1 of 3 changes; 1 changed underneath you and 1 failed — those rows stay unsaved.",
    );
    expect(saveSummary(reduceSave(drafts, new Map(), new Map(), []))).toBe("Nothing to save.");
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
