import { describe, expect, it } from "bun:test";
import type { Check } from "./contract";
import { check, step } from "./lifecycle";
import {
  BASE,
  type Fixture,
  fixture,
  type Gap,
  type Lane,
  type Leg,
  LIVES,
  SERVES,
  variant,
} from "./matrix/types";
import { type Ask, EVERYTHING, type Plan, plan } from "./plan";

const edge = variant("edge", { offeredOn: ["aws"], config: {} });
const box = variant("box", { offeredOn: ["aws", "gcp"], config: {} });

const ping: Check = { title: "ping", run: async () => undefined };
const pong: Check = { title: "pong", run: async () => undefined };

function one(name: string, over: Partial<Omit<Fixture, "name" | "concern">> = {}): Fixture {
  return fixture(name, {
    apps: ["web"],
    legs: SERVES,
    checks: [ping],
    on: { aws: { base: true } },
    ...over,
  });
}

type Input = { lane?: Lane; ask?: Partial<Ask>; gaps?: Gap[]; legs?: Leg[] };

function planOf(fixtures: Fixture[], input: Input = {}): Plan {
  return plan({
    fixtures,
    gaps: input.gaps ?? [],
    lane: input.lane ?? "aws",
    legs: input.legs ?? LIVES,
    ask: { ...EVERYTHING, ...input.ask },
  });
}

const cellsOf = (planned: Plan) => planned.cells.map((cell) => cell.name);

describe("the cells a lane runs", () => {
  it("runs the base cell and then each variant the fixture places on the lane's target", () => {
    const placed = one("deploy/node", {
      on: { aws: { base: true, variants: [edge, box] }, gcp: { variants: [box] } },
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
  it("brings a serving cell up, checks it, and destroys it", () => {
    const serving = one("deploy/node", { checks: [ping, pong] });
    expect(titlesOf(planOf([serving]), "deploy/node")).toEqual(["up", "ping", "pong", "destroy"]);
    expect(planOf([serving]).cells[0]?.steps.map((one) => one.leg)).toEqual([
      "up",
      "contract",
      "contract",
      "destroy",
    ]);
  });

  it("replaces and rolls back a living cell, checking it again after each", () => {
    const living = one("lifecycle/next", { legs: LIVES });
    expect(titlesOf(planOf([living]), "lifecycle/next")).toEqual([
      "up",
      "ping",
      "redeploy",
      "redeploy · ping",
      "rollback",
      "rollback · ping",
      "destroy",
    ]);
  });

  it("walks only the legs the lane's target can drive", () => {
    const living = one("lifecycle/next", { legs: LIVES });
    const planned = planOf([living], { legs: SERVES });
    expect(titlesOf(planned, "lifecycle/next")).toEqual(["up", "ping", "destroy"]);
    expect(planned.cells[0]?.legs).toEqual(SERVES);
  });

  it("leaves destroy out of a lane that keeps its cells standing", () => {
    const planned = planOf([one("deploy/node")], { ask: { keep: true } });
    expect(titlesOf(planned, "deploy/node")).toEqual(["up", "ping"]);
    expect(planned.keep).toBe(true);
  });

  it("takes every app of a workspace through each step before the next", () => {
    const workspace = one("deploy/workspace", { apps: ["web", "api"], checks: [ping, pong] });
    expect(titlesOf(planOf([workspace]), "deploy/workspace")).toEqual([
      "up",
      "api: up",
      "ping",
      "pong",
      "api: ping",
      "api: pong",
      "destroy",
      "api: destroy",
    ]);
  });

  it("fits a ladder's own checks around the lifecycle at the points it names", () => {
    const ladder = one("sdk/with-sst", {
      legs: LIVES,
      ladder: {
        refuse: async () => undefined,
        checks: [
          { title: "records", at: "publish", run: async () => undefined },
          { title: "routes", at: "consume", run: async () => undefined },
          { title: "survives", at: "outlive", run: async () => undefined },
          { title: "empties", at: "prune", run: async () => undefined },
        ],
      },
    });
    expect(titlesOf(planOf([ladder]), "sdk/with-sst")).toEqual([
      "refuse",
      "publish · records",
      "up",
      "ping",
      "consume · routes",
      "redeploy",
      "redeploy · ping",
      "redeploy · consume · routes",
      "rollback",
      "rollback · ping",
      "rollback · consume · routes",
      "destroy",
      "outlive · survives",
      "prune · empties",
    ]);
  });
});

describe("what a lane is asked to run", () => {
  const node = one("deploy/node", { on: { aws: { base: true, variants: [edge, box] } } });
  const sdk = one("sdk/node");
  const living = one("lifecycle/next", { legs: LIVES });

  it("runs only the concerns asked for", () => {
    expect(cellsOf(planOf([node, sdk], { ask: { concerns: ["sdk"] } }))).toEqual(["sdk/node"]);
  });

  it("runs only the fixtures named, in matrix order, and refuses one the target does not run", () => {
    const named = planOf([node, sdk], { ask: { fixtures: ["sdk/node", "deploy/node"] } });
    expect(named.cells.map((cell) => cell.fixture)).toEqual([
      "deploy/node",
      "deploy/node",
      "deploy/node",
      "sdk/node",
    ]);
    expect(() => planOf([node, sdk], { ask: { fixtures: ["sdk/next"] } })).toThrow(
      /this target runs no fixture named sdk\/next \(deploy\/node, sdk\/node\)/,
    );
  });

  it("runs only the variants named, base among them", () => {
    expect(cellsOf(planOf([node], { ask: { variants: ["base", "box"] } }))).toEqual([
      "deploy/node",
      "deploy/node-box",
    ]);
  });

  it("refuses a variant no fixture lists, and runs nothing for one only another target runs", () => {
    expect(() => planOf([node], { ask: { variants: ["fastly"] } })).toThrow(
      /no fixture lists a variant named fastly \(base, edge, box\)/,
    );
    expect(cellsOf(planOf([node, sdk], { lane: "vps", ask: { variants: ["edge"] } }))).toEqual([]);
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

  it("samples a group under covering, and runs all of it under full", () => {
    const grouped = [
      one("deploy/a", { on: { aws: { base: true, variants: [edge] } }, sample: { group: "g" } }),
      one("deploy/b", { on: { aws: { base: true, variants: [edge] } }, sample: { group: "g" } }),
    ];
    expect(cellsOf(planOf(grouped))).toHaveLength(4);
    const covering = planOf(grouped, {
      ask: { coverage: "covering", draw: { seed: "1", touched: [] } },
    });
    expect(cellsOf(covering).filter((name) => name.endsWith("-edge"))).toHaveLength(1);
    expect(cellsOf(covering)).toHaveLength(3);
  });
});

function gap(id: string, affects: Gap["affects"], issue?: number): Gap {
  return issue === undefined
    ? { id, reason: `reason for ${id}`, affects }
    : { id, reason: `reason for ${id}`, issue, affects };
}

describe("the gaps a lane expects", () => {
  const node = one("deploy/node", {
    checks: [ping, pong],
    on: { aws: { base: true, variants: [edge] }, vps: { base: true } },
  });
  const living = one("lifecycle/next", { legs: LIVES, on: { aws: { base: true } } });
  const workspace = one("sdk/workspace", {
    apps: ["web", "api"],
    on: { aws: { base: true, variants: [edge] } },
  });
  const matrix = [node, living, workspace];

  it("lists a test under every gap that names it", () => {
    const planned = planOf(matrix, {
      gaps: [
        gap("one", [{ on: ["aws"], fixtures: [node], tests: [step.up] }], 1),
        gap("two", [{ on: ["aws"], fixtures: [node], tests: [step.up] }]),
      ],
    });
    expect(planned.expectations["deploy/node/web"]?.up).toEqual([
      { id: "one", reason: "reason for one", issue: 1 },
      { id: "two", reason: "reason for two" },
    ]);
  });

  it("lists a test once under a gap whose blocks overlap", () => {
    const planned = planOf(matrix, {
      gaps: [
        gap("one", [
          { on: ["aws"], fixtures: [node], tests: [step.up] },
          { on: ["aws"], tests: [step.up] },
        ]),
      ],
    });
    expect(planned.expectations["deploy/node/web"]?.up).toHaveLength(1);
    expect(planned.expectations["sdk/workspace-edge/api"]?.up).toHaveLength(1);
  });

  it("expands a check across the legs a cell checks it on", () => {
    const planned = planOf(matrix, {
      gaps: [gap("one", [{ on: ["aws"], tests: [check(ping)] }])],
    });
    expect(Object.keys(planned.expectations["lifecycle/next/web"] ?? {})).toEqual([
      "ping",
      "redeploy · ping",
      "rollback · ping",
    ]);
    expect(Object.keys(planned.expectations["deploy/node/web"] ?? {})).toEqual(["ping"]);
  });

  it("expands a check across only the legs named", () => {
    const planned = planOf(matrix, {
      gaps: [gap("one", [{ on: ["aws"], tests: [check([ping, pong], ["rollback"])] }])],
    });
    expect(Object.keys(planned.expectations["lifecycle/next/web"] ?? {})).toEqual([
      "rollback · ping",
    ]);
  });

  it("reaches every variant of a fixture unless the block names some", () => {
    const every = planOf(matrix, {
      gaps: [gap("one", [{ on: ["aws"], fixtures: [node], tests: [step.up] }])],
    });
    expect(every.expectations["deploy/node-edge/web"]?.up).toBeDefined();
    const base = planOf(matrix, {
      gaps: [gap("one", [{ on: ["aws"], fixtures: [node], variants: [BASE], tests: [step.up] }])],
    });
    expect(base.expectations["deploy/node/web"]?.up).toBeDefined();
    expect(base.expectations["deploy/node-edge/web"]).toBeUndefined();
  });

  it("reads a block only on the lanes it names", () => {
    const gaps = [gap("one", [{ on: ["vps"], tests: [step.up] }])];
    expect(planOf(matrix, { gaps }).expectations).toEqual({});
    expect(planOf(matrix, { gaps, lane: "vps" }).expectations["deploy/node/web"]).toBeDefined();
  });

  it("expects only what the lane runs", () => {
    const planned = planOf(matrix, {
      gaps: [gap("one", [{ on: ["aws"], tests: [step.up] }])],
      ask: { fixtures: ["deploy/node"] },
    });
    expect(Object.keys(planned.expectations)).toEqual(["deploy/node/web", "deploy/node-edge/web"]);
  });

  it("skips the whole cell a skipping block reaches, and names it under the gap", () => {
    const planned = planOf(matrix, {
      gaps: [
        gap(
          "one",
          [{ on: ["aws"], fixtures: [workspace], variants: [edge], tests: [step.up], skip: true }],
          9,
        ),
      ],
    });
    expect(cellsOf(planned)).not.toContain("sdk/workspace-edge");
    expect(planned.skipped).toEqual({
      "sdk/workspace-edge": [{ id: "one", reason: "reason for one", issue: 9 }],
    });
  });

  it("names a skipped cell once however many blocks of one gap skip it", () => {
    const planned = planOf(matrix, {
      gaps: [
        gap("one", [
          { on: ["aws"], fixtures: [node], tests: [step.up], skip: true },
          { on: ["aws"], variants: [BASE], tests: [step.up], skip: true },
        ]),
      ],
    });
    expect(planned.skipped["deploy/node"]).toHaveLength(1);
  });

  it("names only the skipped cells the lane was asked to run", () => {
    const gaps = [gap("one", [{ on: ["aws"], fixtures: [node], tests: [step.up], skip: true }])];
    expect(Object.keys(planOf(matrix, { gaps }).skipped)).toEqual([
      "deploy/node",
      "deploy/node-edge",
    ]);
    expect(planOf(matrix, { gaps, ask: { variants: [BASE] } }).skipped).toEqual({
      "deploy/node": [{ id: "one", reason: "reason for one" }],
    });
  });

  it("runs a skipped cell when the skips are lifted, still expecting it red", () => {
    const planned = planOf(matrix, {
      gaps: [gap("one", [{ on: ["aws"], fixtures: [node], tests: [step.up], skip: true }])],
      ask: { runSkipped: true },
    });
    expect(cellsOf(planned)).toContain("deploy/node");
    expect(planned.skipped).toEqual({});
    expect(planned.expectations["deploy/node/web"]?.up).toBeDefined();
  });
});

describe("a gap that reaches nothing", () => {
  const node = one("deploy/node", { on: { aws: { variants: [edge] }, vps: { base: true } } });
  const living = one("lifecycle/next", { legs: LIVES });

  it("refuses a fixture that plans none of the tests named", () => {
    expect(() =>
      planOf([node, living], {
        gaps: [gap("one", [{ on: ["aws"], fixtures: [node], tests: [step.redeploy] }])],
      }),
    ).toThrow(/one on aws lists deploy\/node, which plans none of the tests named/);
  });

  it("refuses a variant the lane does not run", () => {
    expect(() =>
      planOf([node, living], {
        lane: "vps",
        gaps: [gap("one", [{ on: ["vps"], variants: [edge], tests: [step.up] }])],
      }),
    ).toThrow(/one on vps lists edge, which plans none of the tests named/);
  });

  it("refuses a leg the target does not drive", () => {
    expect(() =>
      planOf([node, living], {
        legs: SERVES,
        gaps: [gap("one", [{ on: ["aws"], tests: [check(ping, ["rollback"])] }])],
      }),
    ).toThrow(/one on aws lists nothing that is planned/);
  });

  it("refuses a fixture the lane never runs, even when the lane is asked for less", () => {
    expect(() =>
      planOf([node, living], {
        lane: "vps",
        gaps: [gap("one", [{ on: ["vps"], fixtures: [living], tests: [step.up] }])],
      }),
    ).toThrow(/one on vps lists lifecycle\/next/);
    expect(() =>
      planOf([node, living], {
        gaps: [gap("one", [{ on: ["aws"], fixtures: [node], tests: [step.up] }])],
        ask: { fixtures: ["lifecycle/next"] },
      }),
    ).not.toThrow();
  });

  it("refuses two gaps of one id, and a gap that affects nothing", () => {
    expect(() =>
      planOf([node], {
        gaps: [gap("one", [{ on: ["aws"], tests: [step.up] }]), gap("one", [])],
      }),
    ).toThrow(/the gap one is listed twice/);
    expect(() => planOf([node], { gaps: [gap("one", [])] })).toThrow(/the gap one affects nothing/);
  });
});

describe("a matrix that cannot be planned", () => {
  it("refuses a fixture listed twice", () => {
    expect(() => planOf([one("deploy/node"), one("deploy/node")])).toThrow(
      /deploy\/node is listed twice/,
    );
  });

  it("refuses a variant placed twice on one target", () => {
    expect(() =>
      planOf([one("deploy/node", { on: { aws: { variants: [edge, box, edge] } } })]),
    ).toThrow(/deploy\/node places the edge variant twice on aws/);
  });

  it("refuses a variant the target does not offer", () => {
    expect(() =>
      planOf([one("deploy/node", { on: { vps: { base: true, variants: [edge] } } })], {
        lane: "vps",
      }),
    ).toThrow(/deploy\/node asks vps for the edge variant, which only aws offers/);
  });

  it("refuses a placement that runs nothing, and a fixture placed nowhere", () => {
    expect(() => planOf([one("deploy/node", { on: { aws: {} } })])).toThrow(
      /deploy\/node runs nothing on aws/,
    );
    expect(() => planOf([one("deploy/node", { on: {} })])).toThrow(
      /deploy\/node runs on no target/,
    );
  });

  it("refuses a sample group led by two members", () => {
    const led = (name: string) => one(name, { sample: { group: "g", lead: true } });
    expect(() => planOf([led("deploy/a"), led("deploy/b")])).toThrow(
      /the deploy\/g group is led by deploy\/a and deploy\/b/,
    );
    expect(() => planOf([led("deploy/a"), led("sdk/b")])).not.toThrow();
  });

  it("refuses a fixture path outside the concerns", () => {
    expect(() => one("console/node")).toThrow(/console\/node is no fixture path/);
    expect(() => one("deploy/node/web")).toThrow(/is no fixture path/);
  });
});
