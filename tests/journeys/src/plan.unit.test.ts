import { describe, expect, it } from "vitest";
import { contractRows } from "./contract";
import { DESTROY_TITLE, planTests, REDEPLOY_TITLE, ROLLBACK_TITLE, UP_TITLE } from "./plan";
import { type Leg, specByName } from "./spec";

const ALL_LEGS: Leg[] = ["up", "contract", "redeploy", "rollback", "destroy"];

const STAMP = "GET /api/probes/env reports the greeting and never the secret";

describe("planning a workspace row", () => {
  const workspace = specByName("workspace");
  const planned = planTests([workspace], ALL_LEGS);

  it("plans the whole contract once per app", () => {
    for (const app of workspace.apps) {
      const titles = planned
        .filter((entry) => entry.cell === `workspace/${app}` && entry.leg === "contract")
        .map((entry) => entry.title);
      expect(titles).toEqual(contractRows(workspace.suites).map((row) => row.title));
    }
  });

  it("gives every app its own lifecycle rows, so one can be red while another is green", () => {
    for (const app of workspace.apps) {
      const titles = planned
        .filter((entry) => entry.cell === `workspace/${app}`)
        .map((entry) => entry.title);
      expect(titles).toContain(UP_TITLE);
      expect(titles).toContain(REDEPLOY_TITLE);
      expect(titles).toContain(ROLLBACK_TITLE);
      expect(titles).toContain(DESTROY_TITLE);
    }
  });

  it("names the apps after the frameworks the project mounts", () => {
    expect(workspace.apps).toEqual(["next", "express", "hono"]);
  });
});

describe("planning a hello row", () => {
  const hello = specByName("hello-express");
  const planned = planTests([hello], ALL_LEGS);
  const titles = planned.map((entry) => entry.title);

  it("carries no stamp row", () => {
    expect(titles.some((title) => title.endsWith(STAMP))).toBe(false);
  });

  it("still runs redeploy and rollback with the contract after each", () => {
    expect(titles).toContain(REDEPLOY_TITLE);
    expect(titles).toContain(ROLLBACK_TITLE);
    for (const leg of ["redeploy", "rollback"] as const) {
      const rows = planned.filter((entry) => entry.leg === leg && entry.title.includes(" · "));
      expect(rows.map((entry) => entry.title)).toEqual(
        contractRows(hello.suites).map((row) => `${leg} · ${row.title}`),
      );
    }
  });

  it("asserts only that health and static still answer", () => {
    expect(hello.suites).toEqual(["health", "static"]);
  });
});
