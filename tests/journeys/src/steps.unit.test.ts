import { describe, expect, it } from "bun:test";
import type { Check } from "./checks/context";
import { type Cell, fixture, variant } from "./matrix/types";
import { defaults } from "./matrix/variants";
import { CellRun } from "./run/cellRun";
import type { ExternalStack } from "./stacks";
import { phasesDriven, phasesOf, stepsOf, stepsPlanned, type TestSelector } from "./steps";
import type { Deployment, Target } from "./targets/types";

const ping: Check = { title: "ping", run: async () => undefined };
const living = fixture("lifecycle/next", {
  apps: ["web"],
  redeploys: true,
  checks: [ping],
  on: { aws: [defaults] },
});
const cell: Cell = {
  name: "lifecycle/next",
  fixture: living,
  variant: defaults,
  cacheLayer: "edge",
};
const serving = {
  phases: ["deploy" as const, "verify" as const, "destroy" as const],
  steps: [
    { app: "web", title: "deploy", phase: "deploy" as const },
    { app: "web", title: "ping", phase: "verify" as const },
    { app: "web", title: "destroy", phase: "destroy" as const },
  ],
};

describe("the steps a cell process runs", () => {
  it("runs the steps the plan printed, in its order", () => {
    expect(stepsPlanned(cell, serving).map((step) => step.title)).toEqual([
      "deploy",
      "ping",
      "destroy",
    ]);
  });

  it("refuses a plan whose steps the lifecycle no longer walks", () => {
    const drifted = {
      phases: serving.phases,
      steps: [
        { app: "web", title: "deploy", phase: "deploy" as const },
        { app: "web", title: "pong", phase: "verify" as const },
        { app: "web", title: "destroy", phase: "destroy" as const },
      ],
    };
    expect(() => stepsPlanned(cell, drifted)).toThrow(
      /lifecycle\/next walks web · deploy, web · ping, web · destroy, not the planned web · deploy, web · pong, web · destroy/,
    );
  });
});

describe("the checks a variant adds", () => {
  it("runs a variant's checks after the fixture's own, in every checked phase", () => {
    const shielded = variant("shielded", {
      offeredOn: ["aws"],
      config: {},
      checks: [{ title: "refused without the edge", run: async () => undefined }],
    });
    const titles = stepsOf(
      { name: "lifecycle/next-shielded", fixture: living, variant: shielded, cacheLayer: "edge" },
      ["deploy", "verify", "redeploy"],
    ).map((step) => step.title);
    expect(titles).toEqual([
      "deploy",
      "ping",
      "refused without the edge",
      "redeploy",
      "redeploy · ping",
      "redeploy · refused without the edge",
    ]);
  });
});

describe("the checks a cache layer holds", () => {
  it("runs a check held to one cache layer only on a cell whose cache is served there", () => {
    const layered = fixture("deploy/next", {
      apps: ["web"],
      checks: [
        ping,
        { title: "stamped by the edge", cacheLayer: "edge", run: async () => undefined },
        { title: "stamped by the Next server", cacheLayer: "origin", run: async () => undefined },
      ],
      on: { aws: [defaults] },
    });
    const titlesAt = (cacheLayer: Cell["cacheLayer"]) =>
      stepsOf({ name: "deploy/next", fixture: layered, variant: defaults, cacheLayer }, [
        "verify",
      ]).map((step) => step.title);
    expect(titlesAt("edge")).toEqual(["ping", "stamped by the edge"]);
    expect(titlesAt("origin")).toEqual(["ping", "stamped by the Next server"]);
  });
});

describe("the phases a target drives", () => {
  const replaced = async (): Promise<Deployment> => ({
    baseUrl: () => "",
    fetch: async () => new Response(),
  });
  const redeploying = ["deploy", "verify", "redeploy", "rollback", "destroy"] as const;

  it("drives a redeploy and a rollback on a target with a release cycle", () => {
    expect(() =>
      phasesDriven({ name: "aws", redeploy: replaced, rollback: replaced }, [...redeploying]),
    ).not.toThrow();
    expect(() => phasesDriven({ name: "dev" }, [...serving.phases])).not.toThrow();
  });

  it("refuses a redeploy or rollback on a target with no release cycle", () => {
    expect(() => phasesDriven({ name: "dev" }, [...redeploying])).toThrow(
      /dev walks redeploy, rollback with no release cycle to drive them/,
    );
  });
});

describe("a fixture whose resources restart", () => {
  const restarting = fixture("kv/node", {
    apps: ["web"],
    restarts: true,
    redeploys: true,
    checks: [ping],
    on: { vps: [defaults] },
  });
  const restarted = async (): Promise<Deployment> => ({
    baseUrl: () => "",
    fetch: async () => new Response(),
  });

  it("restarts them after the first checks, and checks again before any redeploy", () => {
    const phases = phasesOf(restarting, false);
    expect(phases).toEqual(["deploy", "verify", "restart", "redeploy", "rollback", "destroy"]);
    const titles = stepsOf(
      { name: "kv/node", fixture: restarting, variant: defaults, cacheLayer: "origin" },
      phases,
    ).map((step) => step.title);
    expect(titles).toEqual([
      "deploy",
      "ping",
      "restart",
      "restart · ping",
      "redeploy",
      "redeploy · ping",
      "rollback",
      "rollback · ping",
      "destroy",
    ]);
  });

  it("fails its restart step, and only that step, on a target with nothing to restart them", async () => {
    const target: Target = {
      name: "aws",
      workers: 1,
      maxRequestBodyBytes: 1,
      stepTimeoutMs: 1,
      sweeper: {
        list: async () => [],
        exists: async () => false,
        sweepStale: async () => {},
        sweepRun: async () => {},
      },
      detectLane: async () => "aws",
      prepareLane: async () => ({}),
      prepareProcess: async () => {},
      deploy: restarted,
      destroy: async () => {},
    };
    const cell: Cell = {
      name: "kv/node",
      fixture: restarting,
      variant: defaults,
      cacheLayer: "origin",
    };
    const run = new CellRun({
      cell,
      target,
      runId: "1",
      keep: false,
      evidence: { dir: "", write: async () => {}, append: async () => {} },
    });
    const [restart] = stepsOf(cell, ["restart"]);
    await expect(restart?.run(run) ?? Promise.resolve()).rejects.toThrow(
      /aws has nothing to restart what an app declared/,
    );
  });
});

describe("a fixture whose build is refused", () => {
  const seen: string[] = [];
  const refused = fixture("kv/node-overlap", {
    apps: ["web"],
    checks: [ping],
    refusal: {
      title: "the build names both entries",
      run: async (said) => {
        seen.push(said);
        if (!said.includes("overlaps")) {
          throw new Error("the refusal names no overlap");
        }
      },
    },
    on: { dev: [defaults] },
  });
  const cell: Cell = {
    name: "kv/node-overlap",
    fixture: refused,
    variant: defaults,
    cacheLayer: "edge",
  };
  const runOn = (deploy: Target["deploy"]) =>
    new CellRun({
      cell,
      target: {
        name: "dev",
        workers: 1,
        maxRequestBodyBytes: 1,
        stepTimeoutMs: 1,
        sweeper: {
          list: async () => [],
          exists: async () => false,
          sweepStale: async () => {},
          sweepRun: async () => {},
        },
        detectLane: async () => "dev",
        prepareLane: async () => ({}),
        prepareProcess: async () => {},
        deploy,
        destroy: async () => {},
      },
      runId: "1",
      keep: false,
      evidence: { dir: "", write: async () => {}, append: async () => {} },
    });

  it("walks its refusal in place of the deploy, then destroys, and checks nothing", () => {
    const phases = phasesOf(refused, false);
    expect(phases).toEqual(["deploy", "destroy"]);
    expect(stepsOf(cell, phases).map((step) => [step.title, step.phase])).toEqual([
      ["the build names both entries", "deploy"],
      ["destroy", "destroy"],
    ]);
  });

  it("passes when the deploy fails with what the refusal looks for", async () => {
    const [refusal] = stepsOf(cell, ["deploy"]);
    const run = runOn(async () => {
      throw new Error('entry "b" has pattern "a/:x", which overlaps pattern ":y/b"');
    });
    await refusal?.run(run);
    expect(seen.at(-1)).toContain("overlaps");
  });

  it("fails when the deploy succeeds, or fails for another reason", async () => {
    const [refusal] = stepsOf(cell, ["deploy"]);
    const deployed = runOn(async () => ({ baseUrl: () => "", fetch: async () => new Response() }));
    await expect(refusal?.run(deployed) ?? Promise.resolve()).rejects.toThrow(
      /dev deployed kv\/node-overlap, whose build is refused/,
    );
    const otherwise = runOn(async () => {
      throw new Error("no docker daemon answers");
    });
    await expect(refusal?.run(otherwise) ?? Promise.resolve()).rejects.toThrow(/names no overlap/);
  });
});

describe("a test a gap names", () => {
  it("is built by the lifecycle, never spelled inline", () => {
    type Accepts<T, U> = [U] extends [T] ? true : false;
    const inline: Accepts<TestSelector, { titles: string[] }> = false;
    expect(inline).toBe(false);
  });
});

describe("a cell with an external stack", () => {
  const calls: string[] = [];
  const said = (what: string) => async () => {
    calls.push(what);
  };
  const deployed = (): Deployment => ({ baseUrl: () => "", fetch: async () => new Response() });

  class RecordingStack implements ExternalStack {
    readonly checks = {
      afterPublish: [{ title: "records", run: said("check records") }],
      whileServing: [{ title: "routes", run: said("check routes") }],
      afterOcelDestroy: [{ title: "survives", run: said("check survives") }],
      afterStackDestroy: [{ title: "empties", run: said("check empties") }],
    };
    async refuse() {
      calls.push("ocel refuses");
    }
    async deploy() {
      calls.push("stack deploys");
    }
    async destroy() {
      calls.push("stack destroys");
    }
    async sweepStale() {}
    async sweepRun() {}
  }

  const stacked = fixture("iac/with-sst", {
    apps: ["web"],
    checks: [{ title: "ping", run: said("check ping") }],
    stack: new RecordingStack(),
    on: { aws: [defaults] },
  });
  const target: Target = {
    name: "aws",
    workers: 1,
    maxRequestBodyBytes: 1,
    stepTimeoutMs: 1,
    sweeper: {
      list: async () => [],
      exists: async () => false,
      sweepStale: async () => {},
      sweepRun: async () => {},
    },
    detectLane: async () => "aws",
    prepareLane: async () => ({}),
    prepareProcess: async () => {},
    deploy: async () => {
      calls.push("ocel deploys");
      return deployed();
    },
    destroy: async () => {
      calls.push("ocel destroys");
    },
  };

  it("deploys the stack before ocel, and destroys it only before the checks that follow it", async () => {
    const cell: Cell = {
      name: stacked.name,
      fixture: stacked,
      variant: defaults,
      cacheLayer: "edge",
    };
    const run = new CellRun({
      cell,
      target,
      runId: "1",
      keep: false,
      evidence: { dir: "", write: async () => {}, append: async () => {} },
    });
    for (const step of stepsOf(cell, phasesOf(stacked, false))) {
      await step.run(run);
    }
    expect(calls).toEqual([
      "ocel refuses",
      "stack deploys",
      "check records",
      "ocel deploys",
      "check ping",
      "check routes",
      "ocel destroys",
      "check survives",
      "stack destroys",
      "check empties",
    ]);
  });
});
