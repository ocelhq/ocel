import { beforeEach, describe, expect, it } from "vitest";

import type { Address, EnvSource, MatrixCell, MatrixRow, State } from "./model";
import { install, type VarsPort } from "./port";

const store = await import("./store");

const cell = (over: Partial<MatrixCell>): MatrixCell => ({
  folder: "",
  state: "required",
  set: false,
  version: 0,
  ...over,
});

const row = (key: string, cells: MatrixCell[]): MatrixRow => ({ key, class: "plain", cells });

const infisical: EnvSource = { id: "infisical:p-1/prod", writable: true };

const stateOf = (envSource: EnvSource): State => ({
  slug: "acme",
  tier: "production",
  other: "preview",
  environments: [],
  envSource,
  matrix: {
    columns: [""],
    rows: [
      row("STRIPE_KEY", [cell({})]),
      row("DATABASE_URL", [cell({ set: true, version: 2, envSource: infisical.id })]),
    ],
    apps: [],
  },
});

interface Sent {
  verb: "set" | "create" | "remove";
  at: Address;
  value?: string;
}

function record(current: State, awaitingApproval = false): Sent[] {
  const sent: Sent[] = [];
  const port: VarsPort = {
    read: async () => current,
    reveal: async () => ({ values: [], errors: [] }),
    set: async (at, value) => {
      sent.push({ verb: "set", at, value });
    },
    create: async (at, value) => {
      sent.push({ verb: "create", at, value });
      return { awaitingApproval };
    },
    remove: async (at) => {
      sent.push({ verb: "remove", at });
    },
    history: async () => [],
    other: async () => ({ tier: "preview", values: [] }),
    copy: async () => [],
  };
  install(port);
  return sent;
}

const at = (key: string, environment = ""): Address => ({ key, folder: "", environment });

beforeEach(() => {
  store.state.value = stateOf(infisical);
  store.drafts.value = new Map();
  store.baselines.value = new Map();
  store.problems.value = new Map();
  store.extras.value = [];
  store.outcome.value = null;
  store.saving.value = false;
  store.removing.value = null;
});

describe("saving under an env source", () => {
  it("creates a missing value in the source rather than writing it to ocel", async () => {
    const sent = record(stateOf(infisical));
    store.setDraft(at("STRIPE_KEY"), "sk_live");
    await store.save();
    expect(sent).toEqual([{ verb: "create", at: at("STRIPE_KEY"), value: "sk_live" }]);
    expect(store.outcome.value?.text).toContain("infisical:p-1/prod");
  });

  it("says when the source holds a created value for approval", async () => {
    record(stateOf(infisical), true);
    store.setDraft(at("STRIPE_KEY"), "sk_live");
    await store.save();
    expect(store.outcome.value).toEqual({
      text: "STRIPE_KEY waits for approval in infisical:p-1/prod before ocel can read it.",
      tone: "owed",
    });
  });

  it("never offers to remove a value the source owns", () => {
    record(stateOf(infisical));
    store.askRemoval([at("DATABASE_URL")]);
    expect(store.removing.value).toBeNull();
  });
});
