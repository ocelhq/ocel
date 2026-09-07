import { describe, expect, it } from "bun:test";
import {
  CONCERNS,
  cellNameOf,
  cellsOf,
  concernsAsked,
  type FixtureSpec,
  fixtureNameOf,
  fixturesNamed,
  groupKeyOf,
  groups,
  LIVES,
  legsKept,
  legsOf,
  preferredOf,
  SERVES,
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
    legs: SERVES,
  },
  {
    name: "node",
    concern: "sdk",
    dir: "sdk/node",
    runtime: "node",
    kind: "composite",
    rows: [],
    apps: [],
    legs: LIVES,
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
    expect(concernsAsked("sdk lifecycle deploy")).toEqual(["deploy", "lifecycle", "sdk"]);
    expect(concernsAsked("lifecycle")).toEqual(["lifecycle"]);
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
    expect(fixturesNamed(rows, "deploy/node").map((row) => row.dir)).toEqual(["deploy/node"]);
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
    expect(() => fixturesNamed(rows, "deploy/node,sdk/next")).toThrow(/no fixture named sdk\/next/);
  });
});

describe("the variants a fixture lists", () => {
  const node = specByName("sdk", "node");
  const next = specByName("sdk", "next");
  const transforms = specByName("sdk", "with-transforms");

  it("is none for a row that lists none", () => {
    expect(variantsOf(rows[0] as FixtureSpec, "aws")).toEqual([]);
    expect(names(rows[0] as FixtureSpec, "aws")).toEqual(["deploy/node"]);
  });

  it("is what the row lists and the target runs", () => {
    expect(variantsOf(node, "aws")).toEqual([container, apiGateway]);
    expect(variantsOf(node, "vps")).toEqual([]);
    expect(variantsOf(node, "dev")).toEqual([]);
    expect(variantsOf(transforms, "aws")).toEqual([container, apiGateway]);
    expect(variantsOf(next, "aws")).toEqual([container, cloudflare]);
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
    expect(names(next, "aws")).toEqual(["sdk/next", "sdk/next-container", "sdk/next-cloudflare"]);
    expect(names(node, "vps")).toEqual(["sdk/node"]);
    expect(names(node, "dev")).toEqual(["sdk/node"]);
  });

  it("runs the base cell only on the targets the row names, and every target when it names none", () => {
    expect(names(node, "aws")).toEqual(["sdk/node-container", "sdk/node-api-gateway"]);
    expect(names(specByName("deploy", "node"), "aws")).toEqual([
      "deploy/node-container",
      "deploy/node-api-gateway",
    ]);
    expect(names(specByName("deploy", "node"), "dev")).toEqual(["deploy/node"]);
    expect(names(specByName("deploy", "node"), "vps")).toEqual(["deploy/node"]);
    expect(names(next, "dev")).toEqual(["sdk/next"]);
  });

  it("calls the base cell's variant base", () => {
    expect(cellsOf(next, "aws").map(variantNameOf)).toEqual(["base", "container", "cloudflare"]);
    expect(cellsOf(node, "aws").map(variantNameOf)).toEqual(["container", "api-gateway"]);
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

  it("names the legs of every row, and only the lifecycle concern and the ladders live", () => {
    for (const row of spec) {
      expect(row.legs.length).toBeGreaterThan(0);
      expect(row.legs).toEqual(
        row.concern === "lifecycle" || row.kind === "ladder" ? LIVES : SERVES,
      );
    }
  });

  it("refuses a name the concern does not carry", () => {
    expect(() => specByName("deploy", "with-sst")).toThrow(/no deploy fixture named with-sst/);
  });
});

describe("the legs a cell runs", () => {
  const lifecycle = specByName("lifecycle", "next");
  const sdkNext = specByName("sdk", "next");

  it("is what the fixture lists, kept in the fixture's order", () => {
    expect(legsOf(lifecycle, LIVES)).toEqual(LIVES);
    expect(legsOf(lifecycle, ["destroy", "contract", "up", "rollback", "redeploy"])).toEqual(LIVES);
  });

  it("drops a leg the target cannot run", () => {
    expect(legsOf(lifecycle, SERVES)).toEqual(SERVES);
  });

  it("drops a leg the fixture does not ask for, whatever the target offers", () => {
    expect(legsOf(sdkNext, LIVES)).toEqual(SERVES);
  });
});

describe("the legs a run drives when it keeps the cell standing", () => {
  it("hands back every leg the target can drive when it destroys", () => {
    expect(legsKept(LIVES, false)).toEqual(LIVES);
  });

  it("drops only destroy when it keeps", () => {
    expect(legsKept(LIVES, true)).toEqual(["up", "contract", "redeploy", "rollback"]);
    expect(legsKept(SERVES, true)).toEqual(["up", "contract"]);
  });
});
