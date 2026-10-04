import { afterEach, describe, expect, it } from "bun:test";
import { existsSync, rmSync } from "node:fs";
import path from "node:path";
import { outputRoot, prepareFile } from "../paths";
import { NO_FILTER } from "../plan";
import type { Target } from "../targets/types";
import { finishLane, runJourney } from "./journey";

const RUN_ID = "unit-journey";

function laneThatPrepares(prepared: string[]): Target {
  return {
    name: "dev",
    workers: 1,
    maxRequestBodyBytes: 1,
    stepTimeoutMs: 1_000,
    sweeper: {
      list: async () => [],
      exists: async () => false,
      sweepStale: async () => {},
      sweepRun: async () => {},
    },
    detectLane: async () => "dev",
    prepareLane: async () => {
      prepared.push("lane");
      return {};
    },
    prepareProcess: async () => {},
    deploy: async () => {
      throw new Error("no cell of this lane ever deploys");
    },
    destroy: async () => {},
  };
}

describe("a lane that selects no cell", () => {
  const saved: [string, string | undefined][] = [
    ["GITHUB_RUN_ID", process.env.GITHUB_RUN_ID],
    ["GITHUB_STEP_SUMMARY", process.env.GITHUB_STEP_SUMMARY],
  ];

  afterEach(() => {
    for (const [name, was] of saved) {
      if (was === undefined) {
        delete process.env[name];
      } else {
        process.env[name] = was;
      }
    }
    rmSync(path.join(outputRoot, RUN_ID), { recursive: true, force: true });
  });

  it("bootstraps nothing and still reconciles clean", async () => {
    process.env.GITHUB_RUN_ID = RUN_ID;
    delete process.env.GITHUB_STEP_SUMMARY;
    const prepared: string[] = [];

    const exitCode = await runJourney(laneThatPrepares(prepared), {
      ...NO_FILTER,
      fixtures: ["deploy/node"],
      variants: ["container"],
    });

    expect(exitCode).toBe(0);
    expect(prepared).toEqual([]);
    expect(existsSync(prepareFile(RUN_ID, "dev"))).toBe(false);
  });
});

describe("finishing a lane", () => {
  it("has nothing to say of a lane with no finish of its own", async () => {
    expect(await finishLane(laneThatPrepares([]))).toBeUndefined();
  });

  it("reports what a lane's finish refused", async () => {
    const lane: Target = {
      ...laneThatPrepares([]),
      finishLane: async () => {
        throw new Error("/etc/nginx changed after up.sh wrote it");
      },
    };
    expect(await finishLane(lane)).toBe("/etc/nginx changed after up.sh wrote it");
  });
});
