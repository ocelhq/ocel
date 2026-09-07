import { describe, expect, it } from "bun:test";
import { AWS_BASE } from "../../config";
import { type Cell, cellsOf, specByName, specForTarget } from "../../spec";
import { cellsBySlugPart, despite, sweepPlan } from "./index";

const fixture = specByName("deploy", "node");

function named(name: string): Cell {
  return { name, fixture };
}

describe("cellsBySlugPart", () => {
  it("keys every cell the aws lane runs, and loses none of them", () => {
    const cells = specForTarget("aws").flatMap((row) => cellsOf(row, "aws"));

    expect(cellsBySlugPart(cells).size).toBe(cells.length);
  });

  it("refuses two cells that slug to one part, rather than dropping one", () => {
    expect(() =>
      cellsBySlugPart([named("deploy/node-api-gateway"), named("deploy/node/api-gateway")]),
    ).toThrow(/both slug to deploy-node-api-gateway/);
  });
});

describe("despite", () => {
  it("keeps a failure as a complaint and lets the work after it run", async () => {
    const complaints: string[] = [];
    const ran: string[] = [];

    for (const namespace of ["j-1799-half-deleted", "j-1799-standing"]) {
      await despite(complaints, `${namespace} sweep`, async () => {
        if (namespace.endsWith("half-deleted")) {
          throw new Error("the bootstrap stack is stuck in DELETE_FAILED");
        }
        ran.push(namespace);
      });
    }

    expect(ran).toEqual(["j-1799-standing"]);
    expect(complaints).toEqual([
      "j-1799-half-deleted sweep: Error: the bootstrap stack is stuck in DELETE_FAILED",
    ]);
  });
});

describe("sweepPlan", () => {
  const fixtures = specForTarget("aws");
  const byPart = cellsBySlugPart(fixtures.flatMap((row) => cellsOf(row, "aws")));

  function planOne(slug: string, cell: string | undefined) {
    const { swept, complaints } = sweepPlan([{ slug, cell }], byPart, fixtures, {});
    expect(complaints).toEqual([]);
    const [one] = swept;
    if (!one) {
      throw new Error(`${slug} planned no sweep`);
    }
    return one;
  }

  it("sweeps a slug naming a cell from that cell's own fixture", () => {
    for (const [part, cell] of byPart) {
      expect(planOne(`j-1874-${part}`, part).fixture).toBe(cell.fixture);
    }
  });

  it("sweeps a slug naming a cell through that cell's own edge", () => {
    const [part] =
      [...byPart].find(([, cell]) => cell.variant?.config?.edge === "api-gateway") ?? [];
    if (!part) {
      throw new Error("no aws cell runs the api-gateway edge, so the sweep has no edge to keep");
    }

    expect(planOne(`j-1874-${part}`, part).overlay).toEqual({
      base: AWS_BASE,
      slug: `j-1874-${part}`,
      edge: "api-gateway",
    });
  });

  it("sweeps a slug whose cell left the spec table from the first aws fixture, edgeless", () => {
    const one = planOne("j-local-apigw3-hello-express", undefined);

    expect(one.fixture).toBe(fixtures[0]);
    expect(one.overlay).toEqual({ base: AWS_BASE, slug: "j-local-apigw3-hello-express" });
  });

  it("complains once, and sweeps nothing, when no fixture runs on aws", () => {
    const { swept, complaints } = sweepPlan(
      [
        { slug: "j-1874-gone", cell: undefined },
        { slug: "j-1874-also-gone", cell: undefined },
      ],
      new Map(),
      [],
      {},
    );

    expect(swept).toEqual([]);
    expect(complaints).toEqual([
      "no fixture runs on aws, so nothing destroys j-1874-gone, j-1874-also-gone",
    ]);
  });
});
