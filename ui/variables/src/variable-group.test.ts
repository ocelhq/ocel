import { describe, expect, it } from "vitest";

import {
  blockedVariableGroupColumns,
  catalogueOf,
  type MatrixCell,
  type MatrixRow,
  missingVariableGroupCellsOf,
  optionalGroupsOf,
  type State,
  type VariableGroupPending,
  variableGroupBlockLine,
  variableGroupColumnKey,
  variableGroupStateOf,
  variableGroupStatesOf,
  variableGroupSwitchable,
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

const stateOf = (rows: MatrixRow[], groups: State["matrix"]["groups"] = []): State => ({
  slug: "acme",
  tier: "production",
  other: "preview",
  environments: ["preview"],
  matrix: { columns: ["", "/web"], rows, groups, apps: [] },
});

const pending = (over: Partial<VariableGroupPending> = {}): VariableGroupPending => ({
  drafts: new Map(),
  removals: new Set(),
  switchedOn: new Set(),
  ...over,
});

const github = stateOf(
  [
    row("GITHUB_ID", [cell({ state: "required" }), cell({ folder: "/web" })], { group: "github" }),
    row("GITHUB_SECRET", [cell({ state: "required" }), cell({ folder: "/web" })], {
      group: "github",
    }),
  ],
  [{ key: "github", required: false, description: "sign in with GitHub" }],
);

describe("variableGroupStateOf", () => {
  it("reads a group with no member set as off", () => {
    const derived = variableGroupStateOf(github, "github", "", "", pending());
    expect(derived?.status).toBe("off");
  });

  it("reads a group with one of two required members set as partial", () => {
    const one = stateOf(
      [
        row("GITHUB_ID", [cell({ state: "required", set: true, version: 1 })], {
          group: "github",
        }),
        row("GITHUB_SECRET", [cell({ state: "required" })], { group: "github" }),
      ],
      [{ key: "github", required: false }],
    );
    const derived = variableGroupStateOf(one, "github", "", "", pending());
    expect(derived?.status).toBe("partial");
    expect(derived?.missing.map((member) => member.at.key)).toEqual(["GITHUB_SECRET"]);
  });

  it("reads a group with every required member set as complete", () => {
    const both = stateOf(
      [
        row("GITHUB_ID", [cell({ state: "required", set: true, version: 1 })], {
          group: "github",
        }),
        row("GITHUB_SECRET", [cell({ state: "required", set: true, version: 1 })], {
          group: "github",
        }),
      ],
      [{ key: "github", required: false }],
    );
    expect(variableGroupStateOf(both, "github", "", "", pending())?.status).toBe("complete");
  });

  it("does not mark a group partial for a member that is optional by its own spelling", () => {
    const mixed = stateOf(
      [
        row("GITHUB_ID", [cell({ state: "required", set: true, version: 1 })], {
          group: "github",
        }),
        row("GITHUB_TENANT", [cell({ state: "optional" })], { group: "github" }),
      ],
      [{ key: "github", required: false }],
    );
    const derived = variableGroupStateOf(mixed, "github", "", "", pending());
    expect(derived?.status).toBe("complete");
    expect(derived?.missing).toEqual([]);
  });

  it("reads a group of entirely optional members as off when nothing is set", () => {
    const loose = stateOf(
      [
        row("GITHUB_ID", [cell({ state: "optional" })], { group: "github" }),
        row("GITHUB_TENANT", [cell({ state: "optional" })], { group: "github" }),
      ],
      [{ key: "github", required: false }],
    );
    expect(variableGroupStateOf(loose, "github", "", "", pending())?.status).toBe("off");
  });

  it("does not count an empty draft as a value", () => {
    const drafts = new Map([["GITHUB_ID  ", ""]]);
    expect(variableGroupStateOf(github, "github", "", "", pending({ drafts }))?.status).toBe("off");
    const typed = new Map([["GITHUB_ID  ", "abc"]]);
    expect(variableGroupStateOf(github, "github", "", "", pending({ drafts: typed }))?.status).toBe(
      "partial",
    );
  });

  it("reads a pending removal as no longer set", () => {
    const both = stateOf(
      [
        row("GITHUB_ID", [cell({ state: "required", set: true, version: 1 })], {
          group: "github",
        }),
        row("GITHUB_SECRET", [cell({ state: "required", set: true, version: 1 })], {
          group: "github",
        }),
      ],
      [{ key: "github", required: false }],
    );
    const removals = new Set(["GITHUB_SECRET  "]);
    const derived = variableGroupStateOf(both, "github", "", "", pending({ removals }));
    expect(derived?.status).toBe("partial");
    expect(
      variableGroupStateOf(
        both,
        "github",
        "",
        "",
        pending({ removals: new Set(["GITHUB_ID  ", "GITHUB_SECRET  "]) }),
      )?.status,
    ).toBe("off");
  });

  it("reads a required group with nothing set as off", () => {
    const gated = stateOf(
      [row("STRIPE_KEY", [cell({ state: "required" })], { group: "stripe" })],
      [{ key: "stripe", required: true }],
    );
    expect(variableGroupStateOf(gated, "stripe", "", "", pending())?.status).toBe("off");
  });

  it("reads a required group with one of two required members set as partial", () => {
    const gated = stateOf(
      [
        row("STRIPE_KEY", [cell({ state: "required", set: true, version: 1 })], {
          group: "stripe",
        }),
        row("STRIPE_WEBHOOK", [cell({ state: "required" })], { group: "stripe" }),
      ],
      [{ key: "stripe", required: true }],
    );
    const derived = variableGroupStateOf(gated, "stripe", "", "", pending());
    expect(derived?.status).toBe("partial");
    expect(derived?.missing.map((member) => member.at.key)).toEqual(["STRIPE_WEBHOOK"]);
  });

  it("reads a switched-on group with nothing set as off and remembers the switch", () => {
    const switchedOn = new Set([variableGroupColumnKey("github", "", "")]);
    const derived = variableGroupStateOf(github, "github", "", "", pending({ switchedOn }));
    expect(derived?.status).toBe("off");
    expect(derived?.switchedOn).toBe(true);
  });

  it("never reads a switched-on group of optional members as partial", () => {
    const loose = stateOf(
      [
        row("GITHUB_ID", [cell({ state: "optional" })], { group: "github" }),
        row("GITHUB_TENANT", [cell({ state: "optional" })], { group: "github" }),
      ],
      [{ key: "github", required: false }],
    );
    const switchedOn = new Set([variableGroupColumnKey("github", "", "")]);
    const derived = variableGroupStateOf(loose, "github", "", "", pending({ switchedOn }));
    expect(derived?.status).toBe("off");
    expect(derived?.missing).toEqual([]);
    expect([...blockedVariableGroupColumns([derived!])]).toEqual([]);
  });

  it("resolves a folder column through the root the way the deploy gate does", () => {
    const rooted = stateOf(
      [
        row(
          "GITHUB_ID",
          [cell({ state: "required", set: true, version: 1 }), cell({ folder: "/web" })],
          { group: "github" },
        ),
        row(
          "GITHUB_SECRET",
          [cell({ state: "required", set: true, version: 1 }), cell({ folder: "/web" })],
          { group: "github" },
        ),
      ],
      [{ key: "github", required: false }],
    );
    expect(variableGroupStateOf(rooted, "github", "/web", "", pending())?.status).toBe("complete");
  });

  it("keeps a scoped member on its own folder rather than the root", () => {
    const scoped = stateOf(
      [
        row(
          "GITHUB_ID",
          [cell({ state: "forbidden" }), cell({ folder: "/web", state: "required" })],
          { group: "github", scope: ["/web"] },
        ),
      ],
      [{ key: "github", required: false }],
    );
    const derived = variableGroupStateOf(scoped, "github", "/web", "", pending());
    expect(derived?.members.map((member) => member.at.folder)).toEqual(["/web"]);
    expect(variableGroupStateOf(scoped, "github", "", "", pending())).toBeUndefined();
  });

  it("reads an environment column through its own override and the base value", () => {
    const overridden = stateOf(
      [
        row(
          "GITHUB_ID",
          [
            cell({
              state: "required",
              set: true,
              version: 1,
              overrides: [{ environment: "preview", version: 2 }],
            }),
          ],
          { group: "github" },
        ),
        row("GITHUB_SECRET", [cell({ state: "required" })], { group: "github" }),
      ],
      [{ key: "github", required: false }],
    );
    const derived = variableGroupStateOf(overridden, "github", "", "preview", pending());
    expect(derived?.status).toBe("partial");
    expect(derived?.missing.map((member) => member.at.environment)).toEqual(["preview"]);
  });
});

describe("missingVariableGroupCells", () => {
  it("marks the missing members of a partial group and nothing in a complete one", () => {
    const mixed = stateOf(
      [
        row("GITHUB_ID", [cell({ state: "required", set: true, version: 1 })], {
          group: "github",
        }),
        row("GITHUB_SECRET", [cell({ state: "required" })], { group: "github" }),
        row("STRIPE_KEY", [cell({ state: "required", set: true, version: 1 })], {
          group: "stripe",
        }),
      ],
      [
        { key: "github", required: false },
        { key: "stripe", required: true },
      ],
    );
    const states = variableGroupStatesOf(mixed, "", pending());
    expect([...missingVariableGroupCellsOf(states)]).toEqual(["GITHUB_SECRET  "]);
  });
});

describe("blockedVariableGroupColumns", () => {
  it("blocks only the columns that contain a partial group", () => {
    const perColumn = stateOf(
      [
        row(
          "GITHUB_ID",
          [
            cell({ state: "required", set: true, version: 1 }),
            cell({ folder: "/web", set: true, version: 1 }),
          ],
          { group: "github" },
        ),
        row(
          "GITHUB_SECRET",
          [
            cell({ state: "required", set: true, version: 1 }),
            cell({ folder: "/web", set: true, version: 1 }),
          ],
          { group: "github" },
        ),
      ],
      [{ key: "github", required: false }],
    );
    const states = variableGroupStatesOf(
      perColumn,
      "",
      pending({ removals: new Set(["GITHUB_SECRET  "]) }),
    );
    expect(states.map((derived) => [derived.folder, derived.status])).toEqual([
      ["", "partial"],
      ["/web", "complete"],
    ]);
    expect([...blockedVariableGroupColumns(states)]).toEqual([" "]);
  });

  it("keys a blocked column by its environment as well as its folder", () => {
    const overridden = stateOf(
      [
        row(
          "GITHUB_ID",
          [
            cell({
              state: "required",
              set: true,
              version: 1,
              overrides: [{ environment: "preview", version: 2 }],
            }),
          ],
          { group: "github" },
        ),
        row("GITHUB_SECRET", [cell({ state: "required" })], { group: "github" }),
      ],
      [{ key: "github", required: false }],
    );
    const states = variableGroupStatesOf(overridden, "preview", pending());
    expect([...blockedVariableGroupColumns(states)]).toEqual([" preview", "/web preview"]);
    expect(variableGroupBlockLine(states)).toBe(
      "github in root for preview and github in /web for preview must be complete before saving, or switch the group off.",
    );
  });
});

describe("variableGroupSwitchable", () => {
  it("gives a required group no switch and an optional group one", () => {
    const gated = stateOf(
      [
        row("STRIPE_KEY", [cell({ state: "required" })], { group: "stripe" }),
        row("GITHUB_ID", [cell({ state: "required" })], { group: "github" }),
      ],
      [
        { key: "stripe", required: true },
        { key: "github", required: false },
      ],
    );
    const states = variableGroupStatesOf(gated, "", pending());
    expect(states.map((derived) => [derived.group.key, variableGroupSwitchable(derived)])).toEqual([
      ["stripe", false],
      ["stripe", false],
      ["github", true],
      ["github", true],
    ]);
  });
});

describe("optionalGroupsOf", () => {
  const summary = (current: State, over: Partial<VariableGroupPending> = {}) =>
    optionalGroupsOf(
      current,
      variableGroupStatesOf(current, "", pending(over)),
      catalogueOf(current, []).variants,
      over.removals ?? new Set(),
    );

  it("reads a group nobody has filled or switched on as off", () => {
    expect(summary(github)).toEqual([
      {
        group: github.matrix.groups![0],
        on: "off",
        onIn: [],
        keys: 2,
        missing: 0,
        stored: 0,
        removing: 0,
      },
    ]);
  });

  it("counts each key still to fill once, however many folders inherit it", () => {
    const [group] = summary(github, {
      switchedOn: new Set(["github  ", "github /web "]),
    });
    expect([group?.on, group?.missing]).toEqual(["on", 2]);
  });

  it("counts the stored values switching the group off would remove", () => {
    const stored = stateOf(
      [
        row(
          "GITHUB_ID",
          [cell({ state: "required", set: true, version: 1 }), cell({ folder: "/web" })],
          {
            group: "github",
          },
        ),
        row(
          "GITHUB_SECRET",
          [cell({ state: "required", set: true, version: 1 }), cell({ folder: "/web" })],
          {
            group: "github",
          },
        ),
      ],
      [{ key: "github", required: false }],
    );
    const [group] = summary(stored);
    expect([group?.on, group?.stored, group?.missing]).toEqual(["on", 2, 0]);
  });

  it("reads a group on in one folder and off at the root as mixed, naming where it is on", () => {
    const mixed = stateOf(
      [
        row(
          "GITHUB_ID",
          [cell({ state: "required" }), cell({ folder: "/web", set: true, version: 1 })],
          {
            group: "github",
          },
        ),
      ],
      [{ key: "github", required: false }],
    );
    const [group] = summary(mixed);
    expect([group?.on, group?.onIn]).toEqual(["mixed", ["/web"]]);
  });

  it("counts the values a switched-off group has staged for removal", () => {
    const [group] = summary(github, { removals: new Set(["GITHUB_ID  ", "OTHER  "]) });
    expect(group?.removing).toBe(1);
  });

  it("leaves required groups out, since nothing can switch them off", () => {
    const required = stateOf(github.matrix.rows, [{ key: "github", required: true }]);
    expect(summary(required)).toEqual([]);
  });
});
