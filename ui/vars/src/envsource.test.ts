import { describe, expect, it } from "vitest";

import {
  type Address,
  addressKey,
  applyDotenv,
  catalogueOf,
  dirtyEntries,
  type EnvSource,
  envSourceGroup,
  type Lens,
  listingOf,
  locked,
  type MatrixCell,
  type MatrixRow,
  planCopy,
  provenanceOf,
  type State,
  stillMissingOf,
  undeclaredOf,
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
  urls: { "": "https://infisical.example/root", "/web": "https://infisical.example/web" },
  credentials: ["INFISICAL_CLIENT_SECRET"],
};

const readingFrom = (
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

const variantOf = (current: State, where: Address) => {
  const found = variantAt(catalogueOf(current, []), where);
  if (!found) throw new Error(`no variant at ${addressKey(where)}`);
  return found;
};

const credentialRow = (over: Partial<MatrixCell> = {}) =>
  row("INFISICAL_CLIENT_SECRET", [cell({ state: "required", ...over })], {
    class: "secret",
    group: envSourceGroup,
  });

describe("who owns a value under an env source", () => {
  const current = readingFrom([
    row("DATABASE_URL", [
      cell({
        state: "required",
        set: true,
        version: 3,
        envSource: infisical.id,
        overrides: [{ environment: "pr-12", version: 1 }],
      }),
      cell({ folder: "/web" }),
    ]),
    credentialRow({ set: true, version: 1 }),
  ]);

  it("gives the env source every class-wide cell, with the URL for its folder", () => {
    expect(variantOf(current, at("DATABASE_URL")).owner).toEqual({
      id: infisical.id,
      writable: false,
      url: "https://infisical.example/root",
    });
    expect(variantOf(current, at("DATABASE_URL", "/web")).owner?.url).toBe(
      "https://infisical.example/web",
    );
  });

  it("leaves a named environment's override to ocel", () => {
    expect(variantOf(current, at("DATABASE_URL", "", "pr-12")).owner).toBeUndefined();
  });

  it("leaves the credential the env source logs in with to ocel", () => {
    expect(variantOf(current, at("INFISICAL_CLIENT_SECRET")).owner).toBeUndefined();
  });

  it("gives nothing away when ocel stores the tier's values itself", () => {
    const builtin = readingFrom(current.matrix.rows, { id: "builtin", writable: false });
    expect(variantOf(builtin, at("DATABASE_URL")).owner).toBeUndefined();
    expect(variantOf(readingFrom(current.matrix.rows, null), at("DATABASE_URL")).owner).toBe(
      undefined,
    );
  });

  it("names where each value comes from", () => {
    expect(provenanceOf(variantOf(current, at("DATABASE_URL")))).toBe(infisical.id);
    expect(provenanceOf(variantOf(current, at("DATABASE_URL", "", "pr-12")))).toBe("builtin");
    expect(provenanceOf(variantOf(current, at("INFISICAL_CLIENT_SECRET")))).toBe("builtin");
    expect(provenanceOf(variantOf(current, at("DATABASE_URL", "/web")))).toBe(infisical.id);
    const bare = readingFrom([row("LOG_LEVEL", [cell({})])], null);
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
  const readOnly = readingFrom(rows);
  const writable = readingFrom(rows, { ...infisical, writable: true });

  it("locks every class-wide cell an env source ocel may not write owns", () => {
    expect(locked(variantOf(readOnly, at("DATABASE_URL")))).toBe(true);
    expect(locked(variantOf(readOnly, at("STRIPE_KEY")))).toBe(true);
    expect(locked(variantOf(readOnly, at("STRIPE_KEY", "", "pr-12")))).toBe(false);
  });

  it("lets a missing value be created in an env source ocel may write, never a present one changed", () => {
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

  it("copies nothing from the other tier into a cell an env source owns", () => {
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

  it("keeps the deploy waiting on no cell only the env source can fill", () => {
    const missing = new Set([addressKey(at("STRIPE_KEY"))]);
    expect(stillMissingOf(catalogueOf(readOnly, []), missing, new Map(), new Map())).toEqual([]);
    expect(
      stillMissingOf(catalogueOf(writable, []), missing, new Map(), new Map()).map((v) => v.at.key),
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
  const current = readingFrom([
    row("DATABASE_URL", [cell({ state: "required", set: true, envSource: infisical.id })]),
    credentialRow(),
  ]);
  const lens: Lens = { environment: "", query: "", unfilledOnly: false };

  it("lists the credentials apart from the values the env source has", () => {
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

  it("lists what an env source has and nothing declares, with the URL to it", () => {
    const undeclared = readingFrom(current.matrix.rows, infisical, {
      matrix: {
        ...current.matrix,
        undeclared: [
          { key: "RETIRED", folder: "/web", envSource: infisical.id },
          { key: "OLD", folder: "/api", envSource: infisical.id },
          { key: "GONE", folder: "", envSource: "exec" },
        ],
      },
    });
    expect(undeclaredOf(undeclared)).toEqual([
      {
        key: "RETIRED",
        folder: "/web",
        envSource: infisical.id,
        url: "https://infisical.example/web",
      },
      { key: "OLD", folder: "/api", envSource: infisical.id },
      { key: "GONE", folder: "", envSource: "exec" },
    ]);
    expect(undeclaredOf(current)).toEqual([]);
  });
});
