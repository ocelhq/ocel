import { describe, expect, it } from "bun:test";
import type { Check } from "./checks/context";
import { phasesDriven, phasesOf, stepsOf, stepsPlanned, type TestRef } from "./lifecycle";
import { fixture } from "./matrix/types";
import { defaults } from "./matrix/variants";
import { CellRun } from "./run/cellRun";
import { ExternalStack } from "./targets/aws/stacks/bindings";
import type { Deployment, Target } from "./targets/types";

const ping: Check = { title: "ping", run: async () => undefined };
const living = fixture("lifecycle/next", {
  apps: ["web"],
  redeploys: true,
  checks: [ping],
  on: { aws: [defaults] },
});
const cell = { name: "lifecycle/next", fixture: living, variant: defaults };
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

describe("a test a gap names", () => {
  it("is built by the lifecycle, never spelled inline", () => {
    type Accepts<T, U> = [U] extends [T] ? true : false;
    const inline: Accepts<TestRef, { titles: string[] }> = false;
    expect(inline).toBe(false);
  });
});

describe("a cell with an external stack", () => {
  const calls: string[] = [];
  const said = (what: string) => async () => {
    calls.push(what);
  };
  const deployed = (): Deployment => ({ baseUrl: () => "", fetch: async () => new Response() });

  class RecordingStack extends ExternalStack {
    override readonly checks = {
      afterPublish: [{ title: "records", run: said("check records") }],
      whileServing: [{ title: "routes", run: said("check routes") }],
      afterOcelDestroy: [{ title: "survives", run: said("check survives") }],
      afterStackDestroy: [{ title: "empties", run: said("check empties") }],
    };
    override async refuse() {
      calls.push("ocel refuses");
    }
    async deploy() {
      calls.push("stack deploys");
    }
    async destroy() {
      calls.push("stack destroys");
    }
    async sweep() {}
  }

  const stacked = fixture("sdk/with-sst", {
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
    const cell = { name: stacked.name, fixture: stacked, variant: defaults };
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
