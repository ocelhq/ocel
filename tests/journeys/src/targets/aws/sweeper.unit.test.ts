import { describe, expect, it } from "bun:test";
import { DEFAULT_BASE } from "../../config";
import { deploy, fixtures as matrix } from "../../matrix/fixtures";
import { type Cell, fixture as fixtureNamed } from "../../matrix/types";
import { defaults } from "../../matrix/variants";
import { cellsOn, fixturesOn } from "../../plan";
import type { ExternalStack } from "../../stacks";
import { bootstrapBlockedBy, cellsBySlugPart, despite, sweepPlan, sweepStacks } from "./sweeper";

const fixture = deploy.node;

function named(name: string): Cell {
  return { name, fixture, variant: defaults };
}

describe("cellsBySlugPart", () => {
  it("keys every cell the aws lane runs, and loses none of them", () => {
    const cells = fixturesOn(matrix, "aws").flatMap((one) => cellsOn(one, "aws"));

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

    for (const namespace of ["j-1799-half-deleted", "j-1799-intact"]) {
      await despite(complaints, `${namespace} sweep`, async () => {
        if (namespace.endsWith("half-deleted")) {
          throw new Error("the bootstrap stack is stuck in DELETE_FAILED");
        }
        ran.push(namespace);
      });
    }

    expect(ran).toEqual(["j-1799-intact"]);
    expect(complaints).toEqual([
      "j-1799-half-deleted sweep: Error: the bootstrap stack is stuck in DELETE_FAILED",
    ]);
  });
});

describe("bootstrapBlockedBy", () => {
  it("keeps the bootstrap when a project in it was not destroyed", () => {
    expect(
      bootstrapBlockedBy("j-1799-deploy-next-cloudflare", ["j-1799-deploy-next-cloudflare"]),
    ).toBe(
      "the j-1799-deploy-next-cloudflare bootstrap was left in place: j-1799-deploy-next-cloudflare could not be destroyed out of it",
    );
  });

  it("lets the bootstrap go when every project in it was destroyed", () => {
    expect(bootstrapBlockedBy("j-1799-deploy-next-cloudflare", [])).toBeUndefined();
  });
});

describe("sweepPlan", () => {
  const fixtures = fixturesOn(matrix, "aws");
  const byPart = cellsBySlugPart(fixtures.flatMap((one) => cellsOn(one, "aws")));

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
    const [part] = [...byPart].find(([, cell]) => cell.variant.config.edge === "api-gateway") ?? [];
    if (!part) {
      throw new Error("no aws cell runs the api-gateway edge, so the sweep has no edge to keep");
    }

    expect(planOne(`j-1874-${part}`, part).overlay).toEqual({
      base: DEFAULT_BASE,
      slug: `j-1874-${part}`,
      edge: "api-gateway",
    });
  });

  it("sweeps a slug whose cell left the matrix from the first aws fixture, edgeless", () => {
    const one = planOne("j-local-apigw3-hello-express", undefined);

    expect(one.fixture).toBe(fixtures[0]);
    expect(one.overlay).toEqual({ base: DEFAULT_BASE, slug: "j-local-apigw3-hello-express" });
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

describe("sweepStacks", () => {
  function stacked(name: string, swept: string[], fails = false) {
    const stack: ExternalStack = {
      checks: { afterPublish: [], whileServing: [], afterOcelDestroy: [], afterStackDestroy: [] },
      deploy: async () => {},
      destroy: async () => {},
      refuse: async () => {},
      sweepStale: async (runId) => {
        swept.push(`${name} stale ${runId}`);
      },
      sweepRun: async (runId) => {
        if (fails) {
          throw new Error(`${name} is stuck`);
        }
        swept.push(`${name} run ${runId}`);
      },
    };
    return fixtureNamed(name, { apps: ["web"], checks: [], stack, on: { aws: [defaults] } });
  }

  it("sweeps the stack of every fixture that provisions one, past one that fails", async () => {
    const swept: string[] = [];
    const complaints: string[] = [];

    await sweepStacks(
      [stacked("iac/stuck", swept, true), fixture, stacked("iac/with-sst", swept)],
      complaints,
      (stack) => stack.sweepRun("1874"),
    );

    expect(swept).toEqual(["iac/with-sst run 1874"]);
    expect(complaints).toEqual(["iac/stuck stack sweep: Error: iac/stuck is stuck"]);
  });
});
