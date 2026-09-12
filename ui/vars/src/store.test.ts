import { beforeEach, describe, expect, it } from "vitest";

import { type Address, addressKey, type MatrixCell, type MatrixRow, type State } from "./model";
import { install, type VarsPort } from "./port";

const store = await import("./store");

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

const pair = (key: string, over: Partial<MatrixCell> = {}) =>
  row(key, [cell({ state: "required", ...over }), cell({ folder: "/web" })], { group: "github" });

const github = () =>
  stateOf(
    [pair("GITHUB_ID"), pair("GITHUB_SECRET")],
    [{ key: "github", required: false, description: "sign in with GitHub" }],
  );

function reset(current: State | null): void {
  store.state.value = current;
  store.environment.value = "";
  store.search.value = "";
  store.owedOnly.value = false;
  store.extras.value = [];
  store.expanded.value = new Set();
  store.focusing.value = null;
  store.drafts.value = new Map();
  store.baselines.value = new Map();
  store.problems.value = new Map();
  store.variableGroupRemovals.value = new Set();
  store.variableGroupsOn.value = new Set();
  store.outcome.value = null;
  store.saving.value = false;
}

const derivedOf = (group: string, folder: string, environment = "") =>
  store.variableGroupStates.value.find(
    (derived) =>
      derived.group.key === group &&
      derived.folder === folder &&
      derived.environment === environment,
  );

const statusOf = (group: string, folder: string, environment = "") =>
  derivedOf(group, folder, environment)?.status;

const switchedOn = (group: string, folder: string, environment = "") =>
  derivedOf(group, folder, environment)?.switchedOn;

beforeEach(() => {
  reset(github());
});

describe("toggleVariableGroup", () => {
  it("turns an off group on, offering its owed members", () => {
    expect(statusOf("github", "")).toBe("off");
    store.toggleVariableGroup("github", "", true);
    expect(switchedOn("github", "")).toBe(true);
    expect(store.focusing.value).toBe("GITHUB_ID  ");
  });

  it("offers every member of a group whose members are all optional", () => {
    reset(
      stateOf(
        [
          row("GITHUB_ID", [cell({ folder: "/web" })], { group: "github" }),
          row("GITHUB_TENANT", [cell({ folder: "/web" })], { group: "github" }),
        ],
        [{ key: "github", required: false }],
      ),
    );
    store.toggleVariableGroup("github", "/web", true);
    expect(switchedOn("github", "/web")).toBe(true);
    expect(statusOf("github", "/web")).toBe("off");
    expect(store.extras.value.map(addressKey)).toEqual(["GITHUB_ID /web ", "GITHUB_TENANT /web "]);
    expect(store.focusing.value).toBe("GITHUB_ID /web ");
  });

  it("materialises the cells a folder column owes when its group is switched on", () => {
    expect(statusOf("github", "/web")).toBe("off");
    store.toggleVariableGroup("github", "/web", true);
    expect(switchedOn("github", "/web")).toBe(true);
    expect(store.extras.value.map(addressKey)).toEqual(["GITHUB_ID /web ", "GITHUB_SECRET /web "]);
    expect(store.focusing.value).toBe("GITHUB_ID /web ");
    expect(store.expanded.value.has("/web")).toBe(true);
  });

  it("turns an on group off, scheduling every held member for removal", () => {
    reset(
      stateOf(
        [
          pair("GITHUB_ID", { set: true, version: 1 }),
          pair("GITHUB_SECRET", { set: true, version: 1 }),
        ],
        [{ key: "github", required: false }],
      ),
    );
    store.toggleVariableGroup("github", "", false);
    expect([...store.variableGroupRemovals.value]).toEqual(["GITHUB_ID  ", "GITHUB_SECRET  "]);
    expect(statusOf("github", "")).toBe("off");
  });

  it("drops the drafts a switched-on group was collecting when it is switched off again", () => {
    store.toggleVariableGroup("github", "", true);
    store.setDraft({ key: "GITHUB_ID", folder: "", environment: "" }, "abc");
    expect(statusOf("github", "")).toBe("partial");
    store.toggleVariableGroup("github", "", false);
    expect(store.drafts.value.size).toBe(0);
    expect(statusOf("github", "")).toBe("off");
  });
});

interface Sent {
  verb: "set" | "remove";
  at: Address;
  value?: string;
  version: number;
}

function record(current: State): Sent[] {
  const sent: Sent[] = [];
  const port: VarsPort = {
    read: async () => current,
    reveal: async () => ({ values: [], errors: [] }),
    set: async (at, value, version) => {
      sent.push({ verb: "set", at, value, version });
    },
    remove: async (at, version) => {
      sent.push({ verb: "remove", at, version });
    },
    history: async () => [],
    other: async () => ({ tier: "preview", values: [] }),
    copy: async () => [],
  };
  install(port);
  return sent;
}

const halfway = () =>
  stateOf(
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
        [cell({ state: "required" }), cell({ folder: "/web", set: true, version: 1 })],
        { group: "github" },
      ),
    ],
    [{ key: "github", required: false }],
  );

describe("save", () => {
  it("holds back only the column whose group is partial", async () => {
    const current = halfway();
    reset(current);
    expect(statusOf("github", "")).toBe("partial");
    expect(statusOf("github", "/web")).toBe("complete");
    store.setDraft({ key: "GITHUB_ID", folder: "", environment: "" }, "root-value");
    store.setDraft({ key: "GITHUB_ID", folder: "/web", environment: "" }, "web-value");
    const sent = record(current);
    await store.save();
    expect(
      sent
        .filter((call) => call.verb === "set")
        .map((call) => ({ ...call.at, value: call.value, version: call.version })),
    ).toEqual([
      { key: "GITHUB_ID", folder: "/web", environment: "", value: "web-value", version: 1 },
    ]);
    expect([...store.drafts.value.keys()]).toEqual(["GITHUB_ID  "]);
    expect(store.outcome.value?.text).toContain("switch the group off");
    expect(store.outcome.value?.tone).toBe("owed");
  });

  it("saves nothing and says why when every pending change sits in a blocked column", async () => {
    const current = halfway();
    reset(current);
    store.setDraft({ key: "GITHUB_ID", folder: "", environment: "" }, "root-value");
    const sent = record(current);
    await store.save();
    expect(sent).toEqual([]);
    expect(store.outcome.value?.text).toBe(
      "github in root must be complete before saving, or switch the group off.",
    );
  });

  it("clears every value a switched-off column resolved through, root included", async () => {
    const current = halfway();
    reset(current);
    store.toggleVariableGroup("github", "/web", false);
    expect(statusOf("github", "/web")).toBe("off");
    expect(statusOf("github", "")).toBe("off");
    const sent = record(current);
    await store.save();
    expect(
      sent
        .filter((call) => call.verb === "remove")
        .map((call) => ({ ...call.at, version: call.version })),
    ).toEqual([
      { key: "GITHUB_ID", folder: "/web", environment: "", version: 1 },
      { key: "GITHUB_ID", folder: "", environment: "", version: 1 },
      { key: "GITHUB_SECRET", folder: "/web", environment: "", version: 1 },
    ]);
  });

  it("keeps a column complete in one folder while another folder switches its group off", async () => {
    const current = stateOf(
      [
        row(
          "GITHUB_ID",
          [cell({ state: "required" }), cell({ folder: "/web", set: true, version: 1 })],
          { group: "github" },
        ),
        row(
          "GITHUB_SECRET",
          [cell({ state: "required" }), cell({ folder: "/web", set: true, version: 1 })],
          { group: "github" },
        ),
      ],
      [{ key: "github", required: false }],
    );
    reset(current);
    expect(statusOf("github", "/web")).toBe("complete");
    expect(statusOf("github", "")).toBe("off");
    store.setDraft({ key: "GITHUB_ID", folder: "/web", environment: "" }, "next");
    const sent = record(current);
    await store.save();
    expect(sent.filter((call) => call.verb === "set")).toHaveLength(1);
    expect(store.outcome.value?.text).toBe("Saved 1 change.");
  });

  it("saves a column whose group a draft completed", async () => {
    const current = halfway();
    reset(current);
    store.setDraft({ key: "GITHUB_SECRET", folder: "", environment: "" }, "secret");
    expect(statusOf("github", "")).toBe("complete");
    const sent = record(current);
    await store.save();
    expect(sent.filter((call) => call.verb === "set")).toHaveLength(1);
    expect(store.outcome.value?.text).toBe("Saved 1 change.");
  });
});

const loose = () =>
  stateOf(
    [
      row("GITHUB_ID", [cell({})], { group: "github" }),
      row("GITHUB_TENANT", [cell({})], { group: "github" }),
      row("DATABASE_URL", [cell({ state: "required" })]),
    ],
    [{ key: "github", required: false }],
  );

describe("save past a group nobody has filled", () => {
  it("saves an unrelated cell beside a required group with nothing set", async () => {
    const current = stateOf(
      [
        row("STRIPE_KEY", [cell({ state: "required" })], { group: "stripe" }),
        row("DATABASE_URL", [cell({ state: "required" })]),
      ],
      [{ key: "stripe", required: true }],
    );
    reset(current);
    expect(statusOf("stripe", "")).toBe("off");
    store.setDraft({ key: "DATABASE_URL", folder: "", environment: "" }, "postgres://");
    const sent = record(current);
    await store.save();
    expect(
      sent
        .filter((call) => call.verb === "set")
        .map((call) => ({ ...call.at, value: call.value, version: call.version })),
    ).toEqual([
      { key: "DATABASE_URL", folder: "", environment: "", value: "postgres://", version: 0 },
    ]);
    expect(store.outcome.value?.text).toBe("Saved 1 change.");
  });

  it("saves an unrelated cell beside a group switched on but not yet typed into", async () => {
    const current = loose();
    reset(current);
    store.toggleVariableGroup("github", "", true);
    store.setDraft({ key: "DATABASE_URL", folder: "", environment: "" }, "postgres://");
    const sent = record(current);
    await store.save();
    expect(sent.filter((call) => call.verb === "set")).toHaveLength(1);
    expect(store.outcome.value?.text).toBe("Saved 1 change.");
    expect(switchedOn("github", "")).toBe(true);
    expect(statusOf("github", "")).toBe("off");
  });

  it("lets go of the switch once the saved matrix carries the group", async () => {
    reset(loose());
    store.toggleVariableGroup("github", "", true);
    store.setDraft({ key: "GITHUB_ID", folder: "", environment: "" }, "id");
    const saved = stateOf(
      [
        row("GITHUB_ID", [cell({ set: true, version: 1 })], { group: "github" }),
        row("GITHUB_TENANT", [cell({})], { group: "github" }),
        row("DATABASE_URL", [cell({ state: "required" })]),
      ],
      [{ key: "github", required: false }],
    );
    record(saved);
    await store.save();
    expect(statusOf("github", "")).toBe("complete");
    expect([...store.variableGroupsOn.value]).toEqual([]);
  });
});

describe("save on a named environment", () => {
  it("gates a base draft on the base column while an override column is complete", async () => {
    const current = halfway();
    reset(current);
    store.environment.value = "preview";
    store.setDraft({ key: "GITHUB_SECRET", folder: "", environment: "preview" }, "for-preview");
    store.setDraft({ key: "GITHUB_ID", folder: "", environment: "" }, "root-value");
    expect(statusOf("github", "", "preview")).toBe("complete");
    expect(statusOf("github", "")).toBe("partial");
    const sent = record(current);
    await store.save();
    expect(
      sent
        .filter((call) => call.verb === "set")
        .map((call) => ({ ...call.at, value: call.value, version: call.version })),
    ).toEqual([
      {
        key: "GITHUB_SECRET",
        folder: "",
        environment: "preview",
        value: "for-preview",
        version: 0,
      },
    ]);
    expect([...store.drafts.value.keys()]).toEqual(["GITHUB_ID  "]);
    expect(store.outcome.value?.text).toContain("github in root must be complete");
  });

  it("holds back an override draft while its own environment column is partial", async () => {
    const current = halfway();
    reset(current);
    store.environment.value = "preview";
    store.setDraft({ key: "GITHUB_ID", folder: "", environment: "preview" }, "for-preview");
    expect(statusOf("github", "", "preview")).toBe("partial");
    const sent = record(current);
    await store.save();
    expect(sent).toEqual([]);
    expect(store.outcome.value?.text).toContain("github in root for preview must be complete");
  });
});

describe("applyDrop", () => {
  it("completes a group through the same derivation, with no special casing", () => {
    const current = halfway();
    reset(current);
    expect(statusOf("github", "")).toBe("partial");
    store.applyDrop("dropped.env", "GITHUB_SECRET=from-the-file\n", "");
    expect(store.drafts.value.get("GITHUB_SECRET  ")).toBe("from-the-file");
    expect(statusOf("github", "")).toBe("complete");
  });

  it("leaves a group partial when the file fills only part of it", () => {
    const current = halfway();
    reset(current);
    store.applyDrop("dropped.env", "GITHUB_ID=from-the-file\n", "");
    expect(statusOf("github", "")).toBe("partial");
  });
});
