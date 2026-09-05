import { describe, expect, it } from "bun:test";
import {
  cellNameOf,
  cellsOf,
  CONCERNS,
  concernsAsked,
  type FixtureSpec,
  fixtureNameOf,
  fixturesNamed,
  groupKeyOf,
  groups,
  preferredOf,
  spec,
  specByName,
  variantNameOf,
  variantsOf,
} from "./spec";
import { apiGateway, cloudflare, container } from "./variants";

const rows: FixtureSpec[] = [
  {
    name: "node",
    concern: "deploy",
    dir: "deploy/node",
    runtime: "node",
    kind: "composite",
    rows: [],
    apps: [],
  },
  {
    name: "node",
    concern: "sdk",
    dir: "sdk/node",
    runtime: "node",
    kind: "composite",
    rows: [],
    apps: [],
  },
];

function names(fixture: FixtureSpec, target: "aws" | "vps" | "dev"): string[] {
  return cellsOf(fixture, target).map((cell) => cell.name);
}

describe("the concerns named in the environment", () => {
  it("is every concern when nothing names one", () => {
    expect(concernsAsked(undefined)).toEqual(CONCERNS);
    expect(concernsAsked("  ")).toEqual(CONCERNS);
  });

  it("takes a space- or comma-separated naming in concern order", () => {
    expect(concernsAsked("sdk deploy")).toEqual(["deploy", "sdk"]);
    expect(concernsAsked("sdk")).toEqual(["sdk"]);
    expect(concernsAsked("deploy,sdk")).toEqual(["deploy", "sdk"]);
  });

  it("refuses a name that is no concern", () => {
    expect(() => concernsAsked("deploy console")).toThrow(/console is no concern/);
  });
});

describe("fixtures named in the environment", () => {
  it("is every row when nothing names one", () => {
    expect(fixturesNamed(rows, undefined)).toEqual(rows);
  });

  it("is every row when the naming is empty", () => {
    expect(fixturesNamed(rows, "  ,  ")).toEqual(rows);
  });

  it("names a fixture by its concern and its name, so the two buckets never collide", () => {
    expect(fixturesNamed(rows, "sdk/node").map((row) => row.dir)).toEqual(["sdk/node"]);
    expect(fixturesNamed(rows, "deploy/node").map((row) => row.dir)).toEqual([
      "deploy/node",
    ]);
  });

  it("keeps spec order, not the order it was named in", () => {
    expect(fixturesNamed(rows, "sdk/node,deploy/node").map((row) => row.dir)).toEqual([
      "deploy/node",
      "sdk/node",
    ]);
  });

  it("tolerates surrounding whitespace", () => {
    expect(fixturesNamed(rows, " sdk/node ").map((row) => row.dir)).toEqual(["sdk/node"]);
  });

  it("refuses a name this target does not run", () => {
    expect(() => fixturesNamed(rows, "deploy/node,sdk/next")).toThrow(
      /no fixture named sdk\/next/,
    );
  });
});

describe("the variants a fixture lists", () => {
  const node = specByName("sdk", "node");
  const transforms = specByName("sdk", "with-transforms");

  it("is none for a row that lists none", () => {
    expect(variantsOf(rows[0] as FixtureSpec, "aws")).toEqual([]);
    expect(names(rows[0] as FixtureSpec, "aws")).toEqual(["deploy/node"]);
  });

  it("is what the row lists and the target runs", () => {
    expect(variantsOf(node, "aws")).toEqual([container, apiGateway, cloudflare]);
    expect(variantsOf(node, "vps")).toEqual([]);
    expect(variantsOf(node, "dev")).toEqual([]);
    expect(variantsOf(transforms, "aws")).toEqual([container, apiGateway, cloudflare]);
  });

  it("refuses a row that lists one variant twice", () => {
    const twice: FixtureSpec = { ...node, variants: [container, apiGateway, container] };
    expect(() => variantsOf(twice, "aws")).toThrow(/lists the container variant twice/);
  });

  it("names a cell after its concern, its fixture and its variant", () => {
    expect(fixtureNameOf(node)).toBe("sdk/node");
    expect(cellNameOf(node, undefined)).toBe("sdk/node");
    expect(cellNameOf(node, container)).toBe("sdk/node-container");
    expect(cellNameOf(specByName("deploy", "workspace"), apiGateway)).toBe(
      "deploy/workspace-api-gateway",
    );
  });

  it("lists the base cell first, then one cell per variant in the order listed", () => {
    expect(names(node, "aws")).toEqual([
      "sdk/node",
      "sdk/node-container",
      "sdk/node-api-gateway",
      "sdk/node-cloudflare",
    ]);
    expect(names(node, "vps")).toEqual(["sdk/node"]);
    expect(names(node, "dev")).toEqual(["sdk/node"]);
  });

  it("calls the base cell's variant base", () => {
    expect(cellsOf(node, "aws").map(variantNameOf)).toEqual([
      "base",
      "container",
      "api-gateway",
      "cloudflare",
    ]);
  });
});

describe("the groups the spec declares", () => {
  it("names one group per concern, and never the same pair twice", () => {
    const keys = groups.map((group) => `${group.concern}/${group.name}`);
    expect(new Set(keys).size).toBe(keys.length);
  });

  it("prefers a fixture of its own concern that carries the group", () => {
    for (const group of groups) {
      const key = `${group.concern}/${group.name}`;
      const members = spec.filter((row) => groupKeyOf(row) === key);
      expect(members.map((row) => row.name)).toContain(group.preferred);
      expect(members.every((row) => row.concern === group.concern)).toBe(true);
      expect(preferredOf(key)).toBe(`${group.concern}/${group.preferred}`);
    }
  });
});

describe("the spec table", () => {
  it("holds one fixture per concern and name, pointing at its own directory", () => {
    const seen = spec.map(fixtureNameOf);
    expect(new Set(seen).size).toBe(seen.length);
    for (const row of spec) {
      expect(row.dir).toBe(`${row.concern}/${row.name}`);
    }
  });

  it("refuses a name the concern does not carry", () => {
    expect(() => specByName("deploy", "with-sst")).toThrow(/no deploy fixture named with-sst/);
  });
});
