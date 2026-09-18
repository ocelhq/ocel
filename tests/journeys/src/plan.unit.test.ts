import { describe, expect, it } from "bun:test";
import type { Check } from "./checks/context";
import { type Fixture, fixture, type Gap, type Lane, variant } from "./matrix/types";
import { defaults } from "./matrix/variants";
import { NO_FILTER, type Plan, plan, type RunFilter } from "./plan";
import type { ExternalStack } from "./stacks";
import { check, step } from "./steps";

const edge = variant("edge", { offeredOn: ["aws"], config: {} });
const box = variant("box", { offeredOn: ["aws", "gcp"], config: {} });

const ping: Check = { title: "ping", run: async () => undefined };

class ProbeStack implements ExternalStack {
  readonly checks = {
    afterPublish: [{ title: "records", run: async () => undefined }],
    whileServing: [{ title: "routes", run: async () => undefined }],
    afterOcelDestroy: [{ title: "survives", run: async () => undefined }],
    afterStackDestroy: [{ title: "empties", run: async () => undefined }],
  };
  async deploy() {}
  async destroy() {}
  async refuse() {}
  async sweep() {}
}
const pong: Check = { title: "pong", run: async () => undefined };

function one(name: string, over: Partial<Omit<Fixture, "name" | "concern">> = {}): Fixture {
  return fixture(name, {
    apps: ["web"],
    checks: [ping],
    on: { aws: [defaults] },
    ...over,
  });
}

type Input = { lane?: Lane; filter?: Partial<RunFilter>; gaps?: Gap[]; releaseCycle?: boolean };

function planOf(fixtures: Fixture[], input: Input = {}): Plan {
  return plan({
    fixtures,
    gaps: input.gaps ?? [],
    lane: input.lane ?? "aws",
    releaseCycle: input.releaseCycle ?? true,
    filter: { ...NO_FILTER, ...input.filter },
  });
}

const cellsOf = (planned: Plan) => planned.cells.map((cell) => cell.name);

describe("the cells a lane runs", () => {
  it("runs each variant the fixture places on the lane's target, the default one unsuffixed", () => {
    const placed = one("deploy/node", {
      on: { aws: [defaults, edge, box], gcp: [box] },
    });
    expect(cellsOf(planOf([placed]))).toEqual([
      "deploy/node",
      "deploy/node-edge",
      "deploy/node-box",
    ]);
    expect(cellsOf(planOf([placed], { lane: "gcp.floci" }))).toEqual(["deploy/node-box"]);
    expect(cellsOf(planOf([placed], { lane: "vps" }))).toEqual([]);
  });
});

const titlesOf = (planned: Plan, cell: string) =>
  planned.cells
    .find((one) => one.name === cell)
    ?.steps.map((one) => (one.app === "web" ? one.title : `${one.app}: ${one.title}`));

describe("the steps a cell walks through", () => {
  it("deploys a cell, verifies it, and destroys it", () => {
    const serving = one("deploy/node", { checks: [ping, pong] });
    expect(titlesOf(planOf([serving]), "deploy/node")).toEqual([
      "deploy",
      "ping",
      "pong",
      "destroy",
    ]);
    expect(planOf([serving]).cells[0]?.steps.map((one) => one.phase)).toEqual([
      "deploy",
      "verify",
      "verify",
      "destroy",
    ]);
    expect(planOf([serving]).cells[0]?.phases).toEqual(["deploy", "verify", "destroy"]);
  });

  it("redeploys and rolls back a cell that redeploys, verifying it again after each", () => {
    const living = one("lifecycle/next", { redeploys: true });
    expect(titlesOf(planOf([living]), "lifecycle/next")).toEqual([
      "deploy",
      "ping",
      "redeploy",
      "redeploy · ping",
      "rollback",
      "rollback · ping",
      "destroy",
    ]);
  });

  it("refuses a cell that redeploys on a target with no release cycle", () => {
    const living = one("lifecycle/next", { redeploys: true });
    expect(() => planOf([living], { releaseCycle: false })).toThrow(
      /lifecycle\/next redeploys, and aws has no release cycle to redeploy it with/,
    );
    expect(cellsOf(planOf([one("deploy/node")], { releaseCycle: false }))).toEqual(["deploy/node"]);
  });

  it("refuses a cell that redeploys only on the targets that cannot, not on the lane's", () => {
    const living = one("lifecycle/next", { redeploys: true, on: { vps: [defaults] } });
    expect(() => planOf([one("deploy/node"), living], { releaseCycle: false })).not.toThrow();
  });

  it("leaves destroy out of a lane that keeps its cells standing", () => {
    const planned = planOf([one("deploy/node")], { filter: { keep: true } });
    expect(titlesOf(planned, "deploy/node")).toEqual(["deploy", "ping"]);
    expect(planned.keep).toBe(true);
  });

  it("takes every app of a workspace through each step before the next", () => {
    const workspace = one("deploy/workspace", { apps: ["web", "api"], checks: [ping, pong] });
    expect(titlesOf(planOf([workspace]), "deploy/workspace")).toEqual([
      "deploy",
      "api: deploy",
      "ping",
      "pong",
      "api: ping",
      "api: pong",
      "destroy",
      "api: destroy",
    ]);
  });

  it("fits an external stack's checks around the lifecycle at the points it names", () => {
    const stacked = one("sdk/with-sst", { redeploys: true, stack: new ProbeStack() });
    expect(titlesOf(planOf([stacked]), "sdk/with-sst")).toEqual([
      "ocel refuses before the stack publishes",
      "after publish · records",
      "deploy",
      "ping",
      "while serving · routes",
      "redeploy",
      "redeploy · ping",
      "redeploy · while serving · routes",
      "rollback",
      "rollback · ping",
      "rollback · while serving · routes",
      "destroy",
      "after ocel destroy · survives",
      "after stack destroy · empties",
    ]);
  });
});

describe("what a lane is asked to run", () => {
  const node = one("deploy/node", { on: { aws: [defaults, edge, box] } });
  const sdk = one("sdk/node");
  const living = one("lifecycle/next", { redeploys: true });

  it("runs only the concerns asked for", () => {
    expect(cellsOf(planOf([node, sdk], { filter: { concerns: ["sdk"] } }))).toEqual(["sdk/node"]);
  });

  it("runs only the fixtures named, in matrix order, and refuses one the target does not run", () => {
    const named = planOf([node, sdk], { filter: { fixtures: ["sdk/node", "deploy/node"] } });
    expect(named.cells.map((cell) => cell.fixture)).toEqual([
      "deploy/node",
      "deploy/node",
      "deploy/node",
      "sdk/node",
    ]);
    expect(() => planOf([node, sdk], { filter: { fixtures: ["sdk/next"] } })).toThrow(
      /this target runs no fixture named sdk\/next \(deploy\/node, sdk\/node\)/,
    );
  });

  it("runs only the variants named, the default among them", () => {
    expect(cellsOf(planOf([node], { filter: { variants: ["default", "box"] } }))).toEqual([
      "deploy/node",
      "deploy/node-box",
    ]);
  });

  it("refuses a variant no fixture lists, and runs nothing for one only another target runs", () => {
    expect(() => planOf([node], { filter: { variants: ["fastly"] } })).toThrow(
      /no fixture lists a variant named fastly \(default, edge, box\)/,
    );
    expect(cellsOf(planOf([node, sdk], { lane: "vps", filter: { variants: ["edge"] } }))).toEqual(
      [],
    );
  });

  it("runs the cells that live longest first, and keeps matrix order among equals", () => {
    expect(cellsOf(planOf([node, sdk, living]))).toEqual([
      "lifecycle/next",
      "deploy/node",
      "deploy/node-edge",
      "deploy/node-box",
      "sdk/node",
    ]);
  });

  it("samples a group when sampled, and runs all of it for every cell", () => {
    const grouped = [
      one("deploy/a", { on: { aws: [defaults, edge] }, sample: { group: "g" } }),
      one("deploy/b", { on: { aws: [defaults, edge] }, sample: { group: "g" } }),
    ];
    expect(cellsOf(planOf(grouped))).toHaveLength(4);
    const sampled = planOf(grouped, {
      filter: { coverage: "sampled", draw: { seed: "1", touched: [] } },
    });
    expect(cellsOf(sampled).filter((name) => name.endsWith("-edge"))).toHaveLength(1);
    expect(cellsOf(sampled)).toHaveLength(3);
  });
});

function gap(id: string, where: Gap["where"], issue?: number): Gap {
  return issue === undefined
    ? { id, reason: `reason for ${id}`, where }
    : { id, reason: `reason for ${id}`, issue, where };
}

describe("the gaps a lane expects", () => {
  const node = one("deploy/node", {
    checks: [ping, pong],
    on: { aws: [defaults, edge], vps: [defaults] },
  });
  const living = one("lifecycle/next", { redeploys: true, on: { aws: [defaults] } });
  const workspace = one("sdk/workspace", {
    apps: ["web", "api"],
    on: { aws: [defaults, edge] },
  });
  const matrix = [node, living, workspace];

  it("lists a test under every gap that names it", () => {
    const planned = planOf(matrix, {
      gaps: [
        gap("one", [{ on: ["aws"], fixtures: [node], fails: [step.deploy] }], 1),
        gap("two", [{ on: ["aws"], fixtures: [node], fails: [step.deploy] }]),
      ],
    });
    expect(planned.expectedFailures["deploy/node/web"]?.deploy).toEqual([
      { id: "one", reason: "reason for one", issue: 1 },
      { id: "two", reason: "reason for two" },
    ]);
  });

  it("lists a test once under a gap whose scopes overlap", () => {
    const planned = planOf(matrix, {
      gaps: [
        gap("one", [
          { on: ["aws"], fixtures: [node], fails: [step.deploy] },
          { on: ["aws"], fails: [step.deploy] },
        ]),
      ],
    });
    expect(planned.expectedFailures["deploy/node/web"]?.deploy).toHaveLength(1);
    expect(planned.expectedFailures["sdk/workspace-edge/api"]?.deploy).toHaveLength(1);
  });

  it("expands a check across the phases a cell verifies it in", () => {
    const planned = planOf(matrix, {
      gaps: [gap("one", [{ on: ["aws"], fails: [check(ping)] }])],
    });
    expect(Object.keys(planned.expectedFailures["lifecycle/next/web"] ?? {})).toEqual([
      "ping",
      "redeploy · ping",
      "rollback · ping",
    ]);
    expect(Object.keys(planned.expectedFailures["deploy/node/web"] ?? {})).toEqual(["ping"]);
  });

  it("expands a check across only the phases named", () => {
    const planned = planOf(matrix, {
      gaps: [gap("one", [{ on: ["aws"], fails: [check([ping, pong], ["rollback"])] }])],
    });
    expect(Object.keys(planned.expectedFailures["lifecycle/next/web"] ?? {})).toEqual([
      "rollback · ping",
    ]);
  });

  it("reaches every variant of a fixture unless the scope names some", () => {
    const every = planOf(matrix, {
      gaps: [gap("one", [{ on: ["aws"], fixtures: [node], fails: [step.deploy] }])],
    });
    expect(every.expectedFailures["deploy/node-edge/web"]?.deploy).toBeDefined();
    const unvaried = planOf(matrix, {
      gaps: [
        gap("one", [{ on: ["aws"], fixtures: [node], variants: [defaults], fails: [step.deploy] }]),
      ],
    });
    expect(unvaried.expectedFailures["deploy/node/web"]?.deploy).toBeDefined();
    expect(unvaried.expectedFailures["deploy/node-edge/web"]).toBeUndefined();
  });

  it("reads a scope only on the lanes it names", () => {
    const gaps = [gap("one", [{ on: ["vps"], fails: [step.deploy] }])];
    expect(planOf(matrix, { gaps }).expectedFailures).toEqual({});
    expect(planOf(matrix, { gaps, lane: "vps" }).expectedFailures["deploy/node/web"]).toBeDefined();
  });

  it("expects only what the lane runs", () => {
    const planned = planOf(matrix, {
      gaps: [gap("one", [{ on: ["aws"], fails: [step.deploy] }])],
      filter: { fixtures: ["deploy/node"] },
    });
    expect(Object.keys(planned.expectedFailures)).toEqual([
      "deploy/node/web",
      "deploy/node-edge/web",
    ]);
  });

  it("skips the whole cell a skipping scope reaches, and names it under the gap", () => {
    const planned = planOf(matrix, {
      gaps: [
        gap(
          "one",
          [
            {
              on: ["aws"],
              fixtures: [workspace],
              variants: [edge],
              fails: [step.deploy],
              skipsCell: true,
            },
          ],
          9,
        ),
      ],
    });
    expect(cellsOf(planned)).not.toContain("sdk/workspace-edge");
    expect(planned.skipped).toEqual({
      "sdk/workspace-edge": [{ id: "one", reason: "reason for one", issue: 9 }],
    });
  });

  it("names a skipped cell once however many scopes of one gap skip it", () => {
    const planned = planOf(matrix, {
      gaps: [
        gap("one", [
          { on: ["aws"], fixtures: [node], fails: [step.deploy], skipsCell: true },
          { on: ["aws"], variants: [defaults], fails: [step.deploy], skipsCell: true },
        ]),
      ],
    });
    expect(planned.skipped["deploy/node"]).toHaveLength(1);
  });

  it("names only the skipped cells the lane was asked to run", () => {
    const gaps = [
      gap("one", [{ on: ["aws"], fixtures: [node], fails: [step.deploy], skipsCell: true }]),
    ];
    expect(Object.keys(planOf(matrix, { gaps }).skipped)).toEqual([
      "deploy/node",
      "deploy/node-edge",
    ]);
    expect(planOf(matrix, { gaps, filter: { variants: ["default"] } }).skipped).toEqual({
      "deploy/node": [{ id: "one", reason: "reason for one" }],
    });
  });

  it("runs a skipped cell when the skips are lifted, still expecting it red", () => {
    const planned = planOf(matrix, {
      gaps: [
        gap("one", [{ on: ["aws"], fixtures: [node], fails: [step.deploy], skipsCell: true }]),
      ],
      filter: { runSkipped: true },
    });
    expect(cellsOf(planned)).toContain("deploy/node");
    expect(planned.skipped).toEqual({});
    expect(planned.expectedFailures["deploy/node/web"]?.deploy).toBeDefined();
  });
});

describe("a gap that reaches nothing", () => {
  const node = one("deploy/node", { on: { aws: [edge], vps: [defaults] } });
  const living = one("lifecycle/next", { redeploys: true });

  it("refuses a fixture that plans none of the tests named", () => {
    expect(() =>
      planOf([node, living], {
        gaps: [gap("one", [{ on: ["aws"], fixtures: [node], fails: [step.redeploy] }])],
      }),
    ).toThrow(/one on aws lists deploy\/node, which plans none of the tests named/);
  });

  it("refuses a variant the lane does not run", () => {
    expect(() =>
      planOf([node, living], {
        lane: "vps",
        gaps: [gap("one", [{ on: ["vps"], variants: [edge], fails: [step.deploy] }])],
      }),
    ).toThrow(/one on vps lists edge, which plans none of the tests named/);
  });

  it("refuses a phase no cell it reaches walks", () => {
    expect(() =>
      planOf([node], {
        gaps: [gap("one", [{ on: ["aws"], fails: [check(ping, ["rollback"])] }])],
      }),
    ).toThrow(/one on aws lists nothing that is planned/);
  });

  it("refuses a fixture the lane never runs, even when the lane is asked for less", () => {
    expect(() =>
      planOf([node, living], {
        lane: "vps",
        gaps: [gap("one", [{ on: ["vps"], fixtures: [living], fails: [step.deploy] }])],
      }),
    ).toThrow(/one on vps lists lifecycle\/next/);
    expect(() =>
      planOf([node, living], {
        gaps: [gap("one", [{ on: ["aws"], fixtures: [node], fails: [step.deploy] }])],
        filter: { fixtures: ["lifecycle/next"] },
      }),
    ).not.toThrow();
  });

  it("refuses two gaps of one id, and a gap that applies nowhere", () => {
    expect(() =>
      planOf([node], {
        gaps: [gap("one", [{ on: ["aws"], fails: [step.deploy] }]), gap("one", [])],
      }),
    ).toThrow(/the gap one is listed twice/);
    expect(() => planOf([node], { gaps: [gap("one", [])] })).toThrow(/the gap one applies nowhere/);
  });
});

describe("a matrix that cannot be planned", () => {
  it("refuses a fixture listed twice", () => {
    expect(() => planOf([one("deploy/node"), one("deploy/node")])).toThrow(
      /deploy\/node is listed twice/,
    );
  });

  it("refuses a variant placed twice on one target", () => {
    expect(() => planOf([one("deploy/node", { on: { aws: [edge, box, edge] } })])).toThrow(
      /deploy\/node places the edge variant twice on aws/,
    );
  });

  it("refuses a variant the target does not offer", () => {
    expect(() =>
      planOf([one("deploy/node", { on: { vps: [defaults, edge] } })], {
        lane: "vps",
      }),
    ).toThrow(/deploy\/node asks vps for the edge variant, which only aws offers/);
  });

  it("refuses a placement that runs nothing, and a fixture placed nowhere", () => {
    expect(() => planOf([one("deploy/node", { on: { aws: [] } })])).toThrow(
      /deploy\/node runs nothing on aws/,
    );
    expect(() => planOf([one("deploy/node", { on: {} })])).toThrow(
      /deploy\/node runs on no target/,
    );
  });

  it("refuses a sample group with two representatives", () => {
    const represents = (name: string) =>
      one(name, { sample: { group: "g", representative: true } });
    expect(() => planOf([represents("deploy/a"), represents("deploy/b")])).toThrow(
      /the deploy\/g group is represented by both deploy\/a and deploy\/b/,
    );
    expect(() => planOf([represents("deploy/a"), represents("sdk/b")])).not.toThrow();
  });

  it("refuses a variant that calls itself the default", () => {
    expect(() => variant("default", { offeredOn: ["aws"], config: {} })).toThrow(
      /default is no variant name/,
    );
  });

  it("refuses a fixture path outside the concerns", () => {
    expect(() => one("console/node")).toThrow(/console\/node is no fixture path/);
    expect(() => one("deploy/node/web")).toThrow(/is no fixture path/);
  });
});
