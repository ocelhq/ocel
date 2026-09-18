import { describe, expect, it } from "bun:test";
import { INITIAL_GREETING, REDEPLOY_GREETING } from "../checks/context";
import type { Evidence } from "../evidence";
import { fixture } from "../matrix/types";
import { defaults } from "../matrix/variants";
import { ExternalStack } from "../targets/aws/stacks/bindings";
import type { Deployment, ReleaseCycle, Target } from "../targets/types";
import { CellRun } from "./cellRun";

type Called = string[];

function deployment(release: string): Deployment {
  return {
    baseUrl: (app) => `https://${app}.${release}.test`,
    fetch: async () => new Response(),
  };
}

function targetFor(called: Called, standing = false): Target & ReleaseCycle {
  return {
    name: "aws",
    workers: 1,
    maxRequestBodyBytes: 7,
    stepTimeoutMs: 1_000,
    sweeper: {
      list: async () => [],
      exists: async () => standing,
      sweepStale: async () => {},
      sweepRun: async () => {},
    },
    detectLane: async () => "aws",
    prepareLane: async () => ({}),
    prepareProcess: async () => {
      called.push("prepare");
    },
    deploy: async () => {
      called.push("deploy");
      return deployment("first");
    },
    redeploy: async (_cell, greeting) => {
      called.push(`redeploy ${greeting}`);
      return deployment("second");
    },
    rollback: async (_cell, greeting) => {
      called.push(`rollback ${greeting}`);
      return deployment("first");
    },
    destroy: async () => {
      called.push("destroy");
    },
  };
}

class RecordingStack extends ExternalStack {
  constructor(private readonly called: Called) {
    super();
  }
  override async refuse(): Promise<void> {
    this.called.push("refuse");
  }
  async deploy(): Promise<void> {
    this.called.push("stack deploy");
  }
  async destroy(): Promise<void> {
    this.called.push("stack destroy");
  }
  async sweep(): Promise<void> {}
}

function recordingEvidence(written: Called): Evidence {
  return {
    dir: "/nowhere",
    write: async (phase, name) => {
      written.push(`${phase}/${name}`);
    },
    append: async () => {},
  };
}

function runOf(
  called: Called,
  over: { keep?: boolean; prepareFailure?: string; standing?: boolean; written?: Called } = {},
) {
  const stacked = fixture("sdk/with-sst", {
    apps: ["web"],
    checks: [],
    stack: new RecordingStack(called),
    on: { aws: [defaults] },
  });
  return new CellRun({
    cell: { name: stacked.name, fixture: stacked, variant: defaults },
    target: targetFor(called, over.standing),
    runId: "1",
    keep: over.keep ?? false,
    evidence: recordingEvidence(over.written ?? []),
    ...(over.prepareFailure === undefined ? {} : { prepareFailure: over.prepareFailure }),
  });
}

describe("a cell run", () => {
  it("deploys once however many steps ask for it", async () => {
    const called: Called = [];
    const run = runOf(called);
    await Promise.all([run.deploy(), run.deploy()]);
    await run.deploy();
    expect(called).toEqual(["deploy"]);
  });

  it("names the cell a target deploys by its slug", () => {
    const run = runOf([]);
    expect(run.slug).toBe("j-1-sdk-with-sst");
  });

  it("refuses a check before the cell is deployed", () => {
    expect(() => runOf([]).verifying("web", "verify")).toThrow(
      /a check ran before the cell was deployed/,
    );
  });

  it("verifies against the release and greeting it last deployed", async () => {
    const called: Called = [];
    const run = runOf(called);
    await run.deploy();
    expect(run.verifying("web", "verify")).toMatchObject({
      app: "web",
      baseUrl: "https://web.first.test",
      greeting: INITIAL_GREETING,
      maxRequestBodyBytes: 7,
      phase: "verify",
    });
    await run.redeploy();
    expect(run.verifying("web", "redeploy")).toMatchObject({
      baseUrl: "https://web.second.test",
      greeting: REDEPLOY_GREETING,
    });
    await run.rollback();
    expect(run.verifying("web", "rollback")).toMatchObject({
      baseUrl: "https://web.first.test",
      greeting: INITIAL_GREETING,
    });
    expect(called).toEqual([
      "deploy",
      `redeploy ${REDEPLOY_GREETING}`,
      `rollback ${INITIAL_GREETING}`,
    ]);
  });

  it("deploys before it redeploys, however the steps are ordered", async () => {
    const called: Called = [];
    await runOf(called).redeploy();
    expect(called).toEqual(["deploy", `redeploy ${REDEPLOY_GREETING}`]);
  });

  it("touches nothing when the lane could not be prepared", async () => {
    const called: Called = [];
    const run = runOf(called, { prepareFailure: "the bootstrap failed" });
    await expect(run.deploy()).rejects.toThrow(/the bootstrap failed/);
    await expect(run.deployStack()).rejects.toThrow(/the bootstrap failed/);
    await expect(run.refuse()).rejects.toThrow(/the bootstrap failed/);
    expect(called).toEqual([]);
  });

  it("touches nothing when the process could not be prepared", async () => {
    const called: Called = [];
    const run = runOf(called);
    const target = targetFor(called);
    const failing = new CellRun({
      cell: { name: "sdk/with-sst", fixture: run.fixture, variant: defaults },
      target: {
        ...target,
        prepareProcess: async () => {
          throw new Error("these credentials resolve to another account");
        },
      },
      runId: "1",
      keep: false,
      evidence: recordingEvidence([]),
    });
    await failing.prepareProcess();
    await expect(failing.refuse()).rejects.toThrow(/another account/);
    await expect(failing.deployStack()).rejects.toThrow(/another account/);
    await expect(failing.deploy()).rejects.toThrow(/another account/);
    expect(called).toEqual([]);
  });

  it("fails a destroy that leaves the cell standing", async () => {
    const run = runOf([], { standing: true });
    await expect(run.destroy()).rejects.toThrow(/j-1-sdk-with-sst still exists on aws/);
  });

  it("destroys the stack once, and never for a cell kept standing", async () => {
    const called: Called = [];
    const run = runOf(called);
    await run.destroyStack();
    await run.destroyStack();
    const kept: Called = [];
    await runOf(kept, { keep: true }).destroyStack();
    expect(called).toEqual(["stack destroy"]);
    expect(kept).toEqual([]);
  });

  it("tears a cell down when it finishes, and records where a kept one stands instead", async () => {
    const called: Called = [];
    await runOf(called).finish(["deploy", "verify", "destroy"]);
    expect(called).toEqual(["destroy"]);

    const kept: Called = [];
    const written: Called = [];
    await runOf(kept, { keep: true, written }).finish(["deploy", "verify"]);
    expect(kept).toEqual([]);
    expect(written).toEqual(["verify/kept.json"]);
  });
});
