import { describe, expect, it } from "bun:test";
import { contractRows } from "./contract";
import {
  cellKey,
  DESTROY_TITLE,
  planTests,
  REDEPLOY_TITLE,
  REFUSE_TITLE,
  ROLLBACK_TITLE,
  UP_TITLE,
} from "./plan";
import {
  type Cell,
  cellsOf,
  type ExampleSpec,
  type LadderRow,
  type Leg,
  specByName,
  suitesOf,
  type TargetName,
} from "./spec";
import { hello } from "./variants";

const ALL_LEGS: Leg[] = ["up", "contract", "redeploy", "rollback", "destroy"];

const STAMP = "GET /api/probes/env reports the greeting and never the secret";

function base(example: ExampleSpec): Cell {
  return { name: example.name, example };
}

describe("planning a workspace row", () => {
  const workspace = specByName("workspace");
  const planned = planTests([base(workspace)], ALL_LEGS);

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
    expect(workspace.apps).toEqual(["next", "express"]);
  });
});

describe("planning a hello cell", () => {
  const express = specByName("express");
  const planned = planTests(cellsOf(express, "vps"), ALL_LEGS);
  const cells = planned.filter((entry) => entry.cell === "express-hello/web");
  const titles = cells.map((entry) => entry.title);

  it("plans a cell of its own, so a hello run never collides with the base one", () => {
    expect(planned.some((entry) => entry.cell === "express/web")).toBe(true);
    expect(cells.length).toBeGreaterThan(0);
  });

  it("carries no stamp row", () => {
    expect(titles.some((title) => title.endsWith(STAMP))).toBe(false);
  });

  it("still runs redeploy and rollback with the contract after each", () => {
    expect(titles).toContain(REDEPLOY_TITLE);
    expect(titles).toContain(ROLLBACK_TITLE);
    for (const leg of ["redeploy", "rollback"] as const) {
      const rows = cells.filter((entry) => entry.leg === leg && entry.title.includes(" · "));
      expect(rows.map((entry) => entry.title)).toEqual(
        contractRows(suitesOf(express, hello)).map((row) => `${leg} · ${row.title}`),
      );
    }
  });

  it("asserts only that health and static still answer", () => {
    expect(suitesOf(express, hello)).toEqual(["health", "static"]);
  });
});

function withHooks(rows: LadderRow[], refuse: boolean): ExampleSpec {
  return {
    name: "with-sst",
    dir: "with-sst",
    framework: "express",
    kind: "ladder",
    suites: ["health", "static", "links"],
    apps: ["web"],
    targets: ["aws"],
    hooks: {
      ...(refuse ? { refuse: async () => undefined } : {}),
      beforeUp: async () => undefined,
      afterDestroy: async () => undefined,
      rows,
    },
  };
}

const publishRow: LadderRow = { title: "lists both records", phase: "publish", run: async () => undefined };
const consumeRow: LadderRow = { title: "both link routes answer", phase: "consume", run: async () => undefined };
const outliveRow: LadderRow = { title: "the record survives", phase: "outlive", run: async () => undefined };
const pruneRow: LadderRow = { title: "both partitions are empty", phase: "prune", run: async () => undefined };

describe("planTests", () => {
  it("plans nothing extra for an example with no hooks", () => {
    const composite: ExampleSpec = {
      name: "express",
      dir: "express",
      framework: "express",
      kind: "composite",
      suites: ["health"],
      apps: ["web"],
    };
    const planned = planTests([base(composite)], [...ALL_LEGS]);
    expect(planned.some((row) => row.title === REFUSE_TITLE)).toBe(false);
    expect(planned.some((row) => row.title.startsWith("publish"))).toBe(false);
  });

  it("plans refuse once, before anything else, for a hooked ladder", () => {
    const example = withHooks([publishRow], true);
    const planned = planTests([base(example)], [...ALL_LEGS]);
    const titles = planned.filter((row) => row.cell === cellKey("with-sst", "web")).map((row) => row.title);
    expect(titles.filter((title) => title === REFUSE_TITLE).length).toBe(1);
    expect(titles.indexOf(REFUSE_TITLE)).toBeLessThan(titles.indexOf("publish · lists both records"));
  });

  it("plans one publish, outlive and prune title but three consume titles", () => {
    const example = withHooks([publishRow, consumeRow, outliveRow, pruneRow], true);
    const planned = planTests([base(example)], [...ALL_LEGS]).map((row) => row.title);
    expect(planned).toContain("publish · lists both records");
    expect(planned).toContain("outlive · the record survives");
    expect(planned).toContain("prune · both partitions are empty");
    expect(planned).toContain("consume · both link routes answer");
    expect(planned).toContain("redeploy · consume · both link routes answer");
    expect(planned).toContain("rollback · consume · both link routes answer");
    expect(planned.filter((title) => title.includes("both link routes answer")).length).toBe(3);
  });

  it("plans no refuse title when the example declares no refuse hook", () => {
    const example = withHooks([publishRow], false);
    const planned = planTests([base(example)], [...ALL_LEGS]);
    expect(planned.some((row) => row.title === REFUSE_TITLE)).toBe(false);
    expect(planned.some((row) => row.title === "publish · lists both records")).toBe(true);
  });
});

describe("planning the cells an example runs on a target", () => {
  const express = specByName("express");

  function cellsOn(target: TargetName): string[] {
    return [...new Set(planTests(cellsOf(express, target), ["up"]).map((row) => row.cell))];
  }

  it("plans one cell per variant beside the base cell on aws", () => {
    expect(cellsOn("aws")).toEqual([
      "express/web",
      "express-hello/web",
      "express-container/web",
      "express-api-gateway/web",
      "express-cloudflare/web",
      "express-hello-api-gateway/web",
    ]);
  });

  it("plans only the variants a target runs", () => {
    expect(cellsOn("vps")).toEqual(["express/web", "express-hello/web"]);
    expect(cellsOn("dev")).toEqual(["express/web"]);
  });

  it("carries the example, app and variant of every test it plans", () => {
    const planned = planTests(cellsOf(specByName("workspace"), "aws"), ["up"]);
    expect(planned.find((row) => row.cell === "workspace-container/express")).toEqual({
      cell: "workspace-container/express",
      example: "workspace",
      app: "express",
      variant: "container",
      title: UP_TITLE,
      leg: "up",
    });
    expect(planned.find((row) => row.cell === "workspace/next")?.variant).toBe("base");
  });
});
