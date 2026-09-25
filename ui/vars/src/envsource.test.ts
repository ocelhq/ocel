import { describe, expect, it } from "vitest";

import {
  type Address,
  addressKey,
  applyDotenv,
  catalogueOf,
  dirtyEntries,
  driftOf,
  type EnvSource,
  listingOf,
  locked,
  type MatrixCell,
  type MatrixRow,
  planCopy,
  provenanceOf,
  type State,
  unfilledOwed,
  variantAt,
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

const infisical: EnvSource = {
  id: "infisical:p-1/prod",
  writable: false,
  links: { "": "https://infisical.example/root", "/web": "https://infisical.example/web" },
  credentials: ["INFISICAL_CLIENT_SECRET"],
};

const sourced = (
  rows: MatrixRow[],
  envSource: EnvSource | null = infisical,
  over: Partial<State> = {},
): State => ({
  slug: "acme",
  tier: "preview",
  other: "production",
  environments: ["pr-12"],
  matrix: { columns: ["", "/web"], rows, apps: [] },
  ...(envSource && { envSource }),
  ...over,
});

const at = (key: string, folder = "", environment = ""): Address => ({ key, folder, environment });

const variantOf = (current: State, where: Address) =>
  variantAt(catalogueOf(current, []), where) ??
  (() => {
    throw new Error(`no variant at ${addressKey(where)}`);
  })();

describe("ownership under an env source", () => {
  const current = sourced([
    row("DATABASE_URL", [
      cell({
        state: "required",
        set: true,
        version: 3,
        envSource: "infisical:p-1/prod",
        overrides: [{ environment: "pr-12", version: 1 }],
      }),
      cell({ folder: "/web" }),
    ]),
    row("INFISICAL_CLIENT_SECRET", [cell({ state: "required", set: true, version: 1 })], {
      class: "secret",
      group: "env source",
    }),
  ]);

  it("gives the source every class-wide cell, with the link for its folder", () => {
    expect(variantOf(current, at("DATABASE_URL")).owner).toEqual({
      id: "infisical:p-1/prod",
      writable: false,
      link: "https://infisical.example/root",
    });
    expect(variantOf(current, at("DATABASE_URL", "/web")).owner?.link).toBe(
      "https://infisical.example/web",
    );
  });

  it("leaves a named environment's override to ocel", () => {
    expect(variantOf(current, at("DATABASE_URL", "", "pr-12")).owner).toBeUndefined();
  });

  it("leaves the credential the source signs in with to ocel", () => {
    expect(variantOf(current, at("INFISICAL_CLIENT_SECRET")).owner).toBeUndefined();
  });

  it("gives nothing away on the builtin store", () => {
    const builtin = sourced(current.matrix.rows, { id: "builtin", writable: false });
    expect(variantOf(builtin, at("DATABASE_URL")).owner).toBeUndefined();
    expect(variantOf(sourced(current.matrix.rows, null), at("DATABASE_URL")).owner).toBeUndefined();
  });

  it("names where each value comes from", () => {
    expect(provenanceOf(variantOf(current, at("DATABASE_URL")))).toBe("infisical:p-1/prod");
    expect(provenanceOf(variantOf(current, at("DATABASE_URL", "", "pr-12")))).toBe("builtin");
    expect(provenanceOf(variantOf(current, at("INFISICAL_CLIENT_SECRET")))).toBe("builtin");
    expect(provenanceOf(variantOf(current, at("DATABASE_URL", "/web")))).toBe("infisical:p-1/prod");
    const bare = sourced([row("LOG_LEVEL", [cell({})])], null);
    expect(provenanceOf(variantOf(bare, at("LOG_LEVEL")))).toBe("");
  });
});

describe("what an owned cell allows", () => {
  const rows = [
    row("DATABASE_URL", [
      cell({ state: "required", set: true, version: 2, envSource: infisical.id }),
    ]),
    row("STRIPE_KEY", [cell({ state: "required" })], { description: "charges cards" }),
  ];
  const readOnly = sourced(rows);
  const writable = sourced(rows, { ...infisical, writable: true });

  it("locks every class-wide cell a source that ocel may not write owns", () => {
    expect(locked(variantOf(readOnly, at("DATABASE_URL")))).toBe(true);
    expect(locked(variantOf(readOnly, at("STRIPE_KEY")))).toBe(true);
    expect(locked(variantOf(readOnly, at("STRIPE_KEY", "", "pr-12")))).toBe(false);
  });

  it("lets a missing value be created in a source ocel may write, never an existing one changed", () => {
    expect(variantOf(writable, at("STRIPE_KEY")).creatable).toBe(true);
    expect(locked(variantOf(writable, at("STRIPE_KEY")))).toBe(false);
    expect(locked(variantOf(writable, at("DATABASE_URL")))).toBe(true);
  });

  it("drops a .env line for a locked cell, naming where to change it", () => {
    const out = applyDotenv(
      catalogueOf(writable, []),
      [
        { key: "DATABASE_URL", value: "postgres://x" },
        { key: "STRIPE_KEY", value: "sk" },
      ],
      "",
    );
    expect(out.fills.map((fill) => fill.at.key)).toEqual(["STRIPE_KEY"]);
    expect(out.skipped).toEqual([
      {
        key: "DATABASE_URL",
        reason: "DATABASE_URL in root is read from infisical:p-1/prod; change it there",
      },
    ]);
  });

  it("copies nothing into a cell a source owns", () => {
    const plan = planCopy(catalogueOf(writable, []), writable.environments, [
      { ...at("STRIPE_KEY"), version: 1, class: "plain", value: "sk" },
      { ...at("STRIPE_KEY", "", "pr-12"), version: 1, class: "plain", value: "sk_pr" },
    ]);
    expect(plan.fills.map((fill) => addressKey(fill.at))).toEqual([
      addressKey(at("STRIPE_KEY", "", "pr-12")),
    ]);
    expect(plan.skipped).toEqual([
      { key: "STRIPE_KEY", reason: "STRIPE_KEY in root is read from infisical:p-1/prod here" },
    ]);
  });

  it("owes nothing here that only the source can fill", () => {
    const owed = new Set([addressKey(at("STRIPE_KEY"))]);
    expect(unfilledOwed(catalogueOf(readOnly, []), owed, new Map(), new Map())).toEqual([]);
    expect(
      unfilledOwed(catalogueOf(writable, []), owed, new Map(), new Map()).map((v) => v.at.key),
    ).toEqual(["STRIPE_KEY"]);
  });

  it("saves no draft typed into a locked cell", () => {
    const drafts = new Map([
      [addressKey(at("DATABASE_URL")), "postgres://mine"],
      [addressKey(at("STRIPE_KEY")), "sk"],
    ]);
    expect(dirtyEntries(catalogueOf(writable, []), drafts, new Map()).map((d) => d.at.key)).toEqual(
      ["STRIPE_KEY"],
    );
  });
});

describe("the env source's own rows", () => {
  const current = sourced([
    row("DATABASE_URL", [cell({ state: "required", set: true, envSource: infisical.id })]),
    row("INFISICAL_CLIENT_SECRET", [cell({ state: "required" })], {
      class: "secret",
      group: "env source",
    }),
  ]);
  const lens = { environment: "", query: "", owedOnly: false };

  it("lists the credentials apart from the values the source holds", () => {
    const listing = listingOf(current, catalogueOf(current, []), new Set(), lens);
    expect(listing.keys.map((line) => line.row.key)).toEqual(["DATABASE_URL"]);
    expect(listing.credentials.map((line) => line.row.key)).toEqual(["INFISICAL_CLIENT_SECRET"]);
  });

  it("keeps a credential in a search's flat list", () => {
    const listing = listingOf(current, catalogueOf(current, []), new Set(), {
      ...lens,
      query: "infisical",
    });
    expect(listing.keys.map((line) => line.row.key)).toEqual(["INFISICAL_CLIENT_SECRET"]);
    expect(listing.credentials).toEqual([]);
  });

  it("names the drift with the link to where it lives", () => {
    const drifting = sourced(current.matrix.rows, infisical, {
      matrix: {
        ...current.matrix,
        drift: [
          { key: "RETIRED", folder: "/web" },
          { key: "OLD", folder: "/api" },
        ],
      },
    });
    expect(driftOf(drifting)).toEqual([
      { key: "RETIRED", folder: "/web", link: "https://infisical.example/web" },
      { key: "OLD", folder: "/api" },
    ]);
    expect(driftOf(current)).toEqual([]);
  });
});
