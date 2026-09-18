import { afterEach, describe, expect, it } from "bun:test";
import { existsSync, rmSync } from "node:fs";
import path from "node:path";
import { outputRoot, prepareFile } from "../paths";
import { EVERYTHING } from "../plan";
import type { Target } from "../targets/types";
import { runJourney } from "./journey";

const RUN_ID = "unit-journey";

function laneThatPrepares(prepared: string[]): Target {
  return {
    name: "dev",
    concurrency: 1,
    largeBodyBytes: 1,
    legTimeoutMs: 1_000,
    guard: async () => "dev",
    prepare: async () => {
      prepared.push("lane");
    },
    setup: async () => {},
    deploy: async () => {
      throw new Error("no cell of this lane ever stands up");
    },
    destroy: async () => {},
    list: async () => [],
    stands: async () => false,
    sweep: async () => {},
    sweepOwn: async () => {},
  };
}

describe("a lane that selects no cell", () => {
  const held: [string, string | undefined][] = [
    ["GITHUB_RUN_ID", process.env.GITHUB_RUN_ID],
    ["GITHUB_STEP_SUMMARY", process.env.GITHUB_STEP_SUMMARY],
  ];

  afterEach(() => {
    for (const [name, was] of held) {
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
      ...EVERYTHING,
      fixtures: ["deploy/node"],
      variants: ["container"],
    });

    expect(exitCode).toBe(0);
    expect(prepared).toEqual([]);
    expect(existsSync(prepareFile(RUN_ID, "dev"))).toBe(false);
  });
});
