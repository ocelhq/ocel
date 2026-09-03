import { describe, expect, it } from "vitest";

import {
  addressKey,
  applyDotenv,
  catalogueOf,
  copyTree,
  dirtyEntries,
  doneLabel,
  environmentsOf,
  held,
  isDirty,
  listingOf,
  names,
  overrideOptions,
  owedCount,
  owedSet,
  planCopy,
  readersOf,
  reduceSave,
  referenceLine,
  removeSummary,
  revealable,
  saveSummary,
  setForOptions,
  sizeLine,
  tallyLine,
  unfilledOwed,
  variantAt,
  variantsOf,
  type Address,
  type Lens,
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

const lens = (over: Partial<Lens> = {}): Lens => ({
  environment: "",
  query: "",
  owedOnly: false,
  ...over,
});

const none = new Set<string>();

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

describe("catalogueOf", () => {
  it("holds every root cell and only the folder cells that are set, required, faulty or overridden", () => {
    const catalogue = catalogueOf(
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
    expect([...catalogue.variants.keys()]).toEqual([
      "A  ",
      "A /api ",
      "B  ",
      "B /web ",
      "C  ",
      "C /web ",
      "D  ",
      "D /web ",
      "D /web qa",
    ]);
    expect(catalogue.variants.get("B  ")?.state).toBe("forbidden");
    expect(catalogue.variants.get("B /web ")).toMatchObject({ kind: "folder", owed: true });
    expect(catalogue.variants.get("C /web ")?.problem).toBe("too short");
    expect(catalogue.variants.get("D /web qa")).toMatchObject({ kind: "environment", set: true, version: 1 });
  });

  it("anchors a key with no root cell on a forbidden root", () => {
    const catalogue = catalogueOf(
      stateOf([row("W", [cell({ folder: "/web", set: true, version: 1 })])]),
      [],
    );
    expect(catalogue.variants.get("W  ")).toMatchObject({ kind: "root", state: "forbidden", set: false });
  });

  it("flags orphaned overrides", () => {
    const catalogue = catalogueOf(
      stateOf([
        row("A", [
          cell({ set: true, version: 5, overrides: [{ environment: "gone", version: 2, orphaned: true }] }),
        ]),
      ]),
      [],
    );
    expect(catalogue.variants.get("A  gone")?.orphaned).toBe(true);
  });

  it("materialises asked-for addresses as empty extras and never duplicates a real one", () => {
    const catalogue = catalogueOf(
      stateOf([row("A", [cell({}), cell({ folder: "/web", set: true, version: 1 })])], ["staging"]),
      [at("A", "/web"), at("A", "", "staging"), at("A", "/web", "staging")],
    );
    expect(variantsOf(catalogue).map((v) => [v.at.folder, v.at.environment, v.extra, v.set])).toEqual([
      ["", "", false, false],
      ["", "staging", true, false],
      ["/web", "", false, true],
      ["/web", "staging", true, false],
    ]);
  });

  it("carries the class down so a secret is never revealable", () => {
    const catalogue = catalogueOf(
      stateOf([
        row("S", [cell({ set: true, version: 1 }), cell({ folder: "/web", set: true, version: 1 })], { class: "secret" }),
        row("P", [cell({ set: true, version: 1 }), cell({ folder: "/web" })]),
      ]),
      [],
    );
    expect(variantsOf(catalogue).map((v) => [v.at.key, v.at.folder, v.class, revealable(v)])).toEqual([
      ["S", "", "secret", false],
      ["S", "/web", "secret", false],
      ["P", "", "plain", true],
    ]);
  });

  it("treats a reference as set", () => {
    const catalogue = catalogueOf(
      stateOf([row("A", [cell({ reference: { slug: "other", folder: "", key: "A" } })])]),
      [],
    );
    expect(catalogue.variants.get("A  ")).toMatchObject({ set: true, reference: { slug: "other" } });
  });

  it("answers any declared address on demand, as an extra when the catalogue lacks it", () => {
    const catalogue = catalogueOf(
      stateOf([row("A", [cell({ set: true, version: 2 }), cell({ folder: "/web" })])], ["qa"]),
      [],
    );
    expect(variantAt(catalogue, at("A"))).toMatchObject({ extra: false, version: 2 });
    expect(variantAt(catalogue, at("A", "/web", "qa"))).toMatchObject({ extra: true, set: false });
    expect(variantAt(catalogue, at("A", "/nope"))).toBeUndefined();
    expect(variantAt(catalogue, at("Z"))).toBeUndefined();
  });
});

describe("listingOf", () => {
  const current = stateOf(
    [
      row("A", [cell({ set: true, version: 3, overrides: [{ environment: "qa", version: 1 }] }), cell({ folder: "/web" }), cell({ folder: "/api", set: true, version: 1 })]),
      row("B", [cell({ state: "forbidden" }), cell({ folder: "/web", state: "required" }), cell({ folder: "/api", state: "forbidden" })], { scope: ["/web"] }),
      row("C", [cell({ state: "required" }), cell({ folder: "/web" })]),
      row("S", [cell({ set: true, version: 1 })], { class: "secret" }),
    ],
    ["qa", "staging"],
    ["", "/web", "/api"],
  );
  const catalogue = catalogueOf(current, []);

  it("lists root keys first, keeping a scoped key as a pointer, then one group per folder", () => {
    const listing = listingOf(current, catalogue, none, lens());
    expect(listing.flat).toBe(false);
    expect(listing.keys.map((line) => [line.row.key, line.variant.state, line.inherits, line.overrides])).toEqual([
      ["A", "optional", null, ["qa"]],
      ["B", "forbidden", null, []],
      ["C", "required", null, []],
      ["S", "optional", null, []],
    ]);
    expect(listing.groups.map((group) => [group.folder, group.keys, group.owed])).toEqual([
      ["/web", 1, 1],
      ["/api", 1, 0],
    ]);
  });

  it("lists in a group only the keys with a cell of their own there, never the ones that merely inherit", () => {
    const listing = listingOf(current, catalogue, none, lens());
    expect(listing.groups.map((group) => group.lines.map((line) => addressKey(line.variant.at)))).toEqual([
      ["B /web "],
      ["A /api "],
    ]);
  });

  it("lists an asked-for cell in its group as an extra that inherits the root", () => {
    const asked = catalogueOf(current, [at("A", "/web")]);
    const listing = listingOf(current, asked, none, lens());
    expect(listing.groups[0]?.lines.map((line) => [line.row.key, line.inherits, line.variant.extra])).toEqual([
      ["A", "root", true],
      ["B", null, false],
    ]);
    expect(listing.groups[0]?.keys).toBe(2);
  });

  it("shows an environment's overrides, or what falls through to the base", () => {
    const listing = listingOf(current, catalogue, none, lens({ environment: "qa" }));
    expect(listing.keys.map((line) => [line.row.key, line.variant.at.environment, line.inherits, line.variant.set])).toEqual([
      ["A", "qa", null, true],
      ["B", "", null, false],
      ["C", "qa", "base", false],
      ["S", "qa", "base", false],
    ]);
    expect(listing.groups[1]?.lines.map((line) => [addressKey(line.variant.at), line.inherits])).toEqual([
      ["A /api qa", "base"],
    ]);
  });

  it("flattens across folders when searching", () => {
    const listing = listingOf(current, catalogue, none, lens({ query: " b " }));
    expect(listing.flat).toBe(true);
    expect(listing.groups).toEqual([]);
    expect(listing.keys.map((line) => addressKey(line.variant.at))).toEqual(["B /web "]);
  });

  it("flattens to the cells a deploy is owed, wherever they live", () => {
    const owed = owedSet({ deploy: "dpl_1", owed: [{ key: "C", folder: "" }, { key: "B", folder: "/web" }] });
    const listing = listingOf(current, catalogue, owed, lens({ owedOnly: true }));
    expect(listing.flat).toBe(true);
    expect(listing.groups).toEqual([]);
    expect(listing.keys.map((line) => [addressKey(line.variant.at), line.needed])).toEqual([
      ["B /web ", true],
      ["C  ", true],
    ]);
  });

  it("offers an override only for environments the address lacks", () => {
    expect(overrideOptions(current, catalogue, at("A"))).toEqual(["staging"]);
    expect(overrideOptions(current, catalogueOf(current, [at("A", "", "staging")]), at("A"))).toEqual([]);
    expect(overrideOptions(current, catalogue, at("C", "/web"))).toEqual(["qa", "staging"]);
  });

  it("offers to set a root key in a folder that reads it and holds no cell yet", () => {
    const rowOf = (key: string) => current.matrix.rows.find((row) => row.key === key)!;
    expect(setForOptions(catalogue, rowOf("A"))).toEqual(["/web"]);
    expect(setForOptions(catalogueOf(current, [at("A", "/web")]), rowOf("A"))).toEqual([]);
    expect(setForOptions(catalogue, rowOf("B"))).toEqual([]);
    expect(setForOptions(catalogue, rowOf("C"))).toEqual(["/web"]);
  });

  it("lists orphaned environments after the live ones", () => {
    const orphaned = stateOf(
      [row("A", [cell({ set: true, version: 1, overrides: [{ environment: "gone", version: 1, orphaned: true }] })])],
      ["qa"],
    );
    expect(environmentsOf(orphaned)).toEqual([
      { name: "qa", orphaned: false },
      { name: "gone", orphaned: true },
    ]);
  });
});

describe("readersOf", () => {
  const apps = [
    { name: "web", folder: "/web" },
    { name: "api", folder: "/api" },
    { name: "root-app", folder: "" },
  ];

  it("names every app for an unscoped key, since each reads the root", () => {
    expect(readersOf(row("A", [cell({}), cell({ folder: "/web" })]), apps).map((a) => a.name)).toEqual([
      "web",
      "api",
      "root-app",
    ]);
  });

  it("names only the apps bound to a scoped key's folders", () => {
    const scoped = row(
      "B",
      [cell({ state: "forbidden" }), cell({ folder: "/web" }), cell({ folder: "/api", state: "forbidden" })],
      { scope: ["/web"] },
    );
    expect(readersOf(scoped, apps).map((a) => a.name)).toEqual(["web"]);
  });
});

describe("referenceLine", () => {
  it("omits an empty folder", () => {
    expect(referenceLine({ slug: "billing", folder: "", key: "K" })).toBe("billing/K");
    expect(referenceLine({ slug: "billing", folder: "/api", key: "K" })).toBe("billing/api/K");
  });
});

describe("sizeLine", () => {
  it("keeps bytes under a kibibyte and rounds above", () => {
    expect(sizeLine(48)).toBe("48 B");
    expect(sizeLine(2048)).toBe("2.0 KiB");
  });
});

describe("applyDotenv", () => {
  const catalogue = catalogueOf(
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
    const out = applyDotenv(catalogue, entries, "");
    expect(out.fills).toEqual([
      { at: at("A"), value: "a", materialise: false },
      { at: at("S"), value: "s", materialise: false },
    ]);
    expect(out.undeclared).toEqual(["NOPE"]);
    expect(out.skipped.map((s) => s.key)).toEqual(["R", "W"]);
    expect(out.skipped[0]?.reason).toMatch(/reads billing.*ocel env ref/);
    expect(out.skipped[1]?.reason).toBe("nothing reads W in root");
  });

  it("scopes to a folder and asks to materialise cells the catalogue lacks", () => {
    const out = applyDotenv(catalogue, entries, "/web");
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
  const catalogue = catalogueOf(
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
    const plan = planCopy(catalogue, ["staging"], [
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
    expect(copyTree(plan).map((branch) => [branch.folder, branch.cells.map((c) => addressKey(c.at))])).toEqual([
      ["", ["S  ", "A  staging", "A  "]],
      ["/web", ["A /web "]],
    ]);
  });

  it("lists unreadable cells rather than dropping them", () => {
    const plan = planCopy(catalogue, [], [other({ key: "A", error: "timed out" })]);
    expect(plan.unreadable).toEqual([{ at: at("A"), error: "timed out" }]);
    expect(plan.fills).toEqual([]);
    expect(plan.overwrites).toEqual([]);
  });

  it("skips what cannot land here and says why", () => {
    const plan = planCopy(catalogue, [], [
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

describe("recovery", () => {
  const catalogue = catalogueOf(
    stateOf(
      [
        row("A", [cell({ set: true, version: 1 }), cell({ folder: "/web" })]),
        row("B", [cell({ state: "required" }), cell({ folder: "/web" })]),
        row("C", [cell({ set: true, version: 2, problem: "bad" }), cell({ folder: "/web" })]),
        row("D", [cell({ state: "forbidden" }), cell({ folder: "/web", state: "required" })], { scope: ["/web"] }),
      ],
      [],
    ),
    [],
  );
  const owed = owedSet({
    deploy: "dpl_1",
    owed: [
      { key: "B", folder: "" },
      { key: "C", folder: "" },
      { key: "D", folder: "/web" },
    ],
  });

  it("counts an owed cell as filled once it is set and valid, or holds a draft", () => {
    expect(unfilledOwed(catalogue, owed, new Map(), new Map()).map((v) => v.at)).toEqual([
      at("B"),
      at("C"),
      at("D", "/web"),
    ]);
    const drafts = new Map([
      [addressKey(at("B")), "b"],
      [addressKey(at("C")), "fixed"],
      [addressKey(at("D", "/web")), "d"],
    ]);
    expect(unfilledOwed(catalogue, owed, drafts, new Map())).toEqual([]);
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
  const catalogue = catalogueOf(
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
    expect(dirtyEntries(catalogue, new Map(), new Map())).toEqual([]);
    expect(
      dirtyEntries(catalogue, new Map([[key(at("A")), "x"]]), new Map([[key(at("A")), "x"]])),
    ).toEqual([]);
  });

  it("carries the version each dirty row was read at, in matrix order", () => {
    const drafts = new Map([
      [key(at("B")), "new"],
      [key(at("A", "/web")), "web"],
      [key(at("A", "", "staging")), "stage"],
      [key(at("A")), ""],
    ]);
    expect(dirtyEntries(catalogue, drafts, new Map())).toEqual([
      { at: at("A", "", "staging"), value: "stage", version: 0 },
      { at: at("A", "/web"), value: "web", version: 1 },
      { at: at("B"), value: "new", version: 0 },
    ]);
  });

  it("never includes a referenced cell, whatever its draft says", () => {
    const linked = catalogueOf(
      stateOf([row("R", [cell({ reference: { slug: "billing", folder: "/api", key: "R" } })])]),
      [],
    );
    expect(dirtyEntries(linked, new Map([[key(at("R")), "typed"]]), new Map())).toEqual([]);
  });

  it("treats clearing a revealed value as a change to empty", () => {
    const baselines = new Map([[key(at("A")), "old"]]);
    const drafts = new Map([[key(at("A")), ""]]);
    expect(dirtyEntries(catalogue, drafts, baselines)).toEqual([
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

  it("summarises a removal the same way", () => {
    expect(removeSummary(reduceSave(new Map(), new Map(), new Map(), [{ at: at("A"), ok: true }]))).toBe(
      "Removed 1 value.",
    );
    expect(removeSummary(reduceSave(new Map(), new Map(), new Map(), results))).toBe(
      "Removed 1 of 3 values; 1 changed underneath you and 1 failed — see the marked rows.",
    );
  });
});

describe("labels", () => {
  it("pluralise the owed count", () => {
    expect(tallyLine(0)).toBe("every required cell is filled");
    expect(tallyLine(1)).toBe("1 cell to fill");
    expect(doneLabel(0)).toBe("Return to the terminal");
    expect(doneLabel(3)).toBe("Return with 3 cells still to fill");
  });
});
