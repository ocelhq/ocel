import { describe, expect, it } from "bun:test";
import {
  cellKey,
  DESTROY_TITLE,
  planTests,
  REDEPLOY_TITLE,
  REFUSE_TITLE,
  ROLLBACK_TITLE,
  UP_TITLE,
} from "./plan";
import { ENV_ROW, healthRows, probeRows, productRows, staticRows } from "./rows";
import {
  type Cell,
  cellsOf,
  type FixtureSpec,
  fixtureNameOf,
  type LadderRow,
  LIVES,
  SERVES,
  specByName,
  type TargetName,
} from "./spec";

function base(fixture: FixtureSpec): Cell {
  return { name: fixtureNameOf(fixture), fixture };
}

describe("planning a workspace row", () => {
  const workspace = specByName("sdk", "workspace");
  const planned = planTests([base(workspace)], LIVES);

  it("plans the whole contract once per app", () => {
    for (const app of workspace.apps) {
      const titles = planned
        .filter((entry) => entry.cell === `sdk/workspace/${app}` && entry.leg === "contract")
        .map((entry) => entry.title);
      expect(titles).toEqual(workspace.rows.map((row) => row.title));
    }
  });

  it("gives every app its own up and destroy, so one can be red while another is green", () => {
    for (const app of workspace.apps) {
      const titles = planned
        .filter((entry) => entry.cell === `sdk/workspace/${app}`)
        .map((entry) => entry.title);
      expect(titles).toContain(UP_TITLE);
      expect(titles).toContain(DESTROY_TITLE);
    }
  });

  it("names the apps the way the project config does", () => {
    expect(workspace.apps).toEqual(["next", "express"]);
  });
});

describe("planning the two concerns of one runtime", () => {
  const deploy = specByName("deploy", "node");
  const sdk = specByName("sdk", "node");
  const planned = planTests([base(deploy), base(sdk)], LIVES);

  it("gives each concern a cell of its own, so neither can shadow the other", () => {
    expect(planned.some((entry) => entry.cell === "deploy/node/web")).toBe(true);
    expect(planned.some((entry) => entry.cell === "sdk/node/web")).toBe(true);
  });

  it("plans the product rows for the sdk cell alone", () => {
    const productTitles = productRows.map((row) => row.title);
    const titlesOf = (cell: string) =>
      planned.filter((entry) => entry.cell === cell && entry.leg === "contract").map((e) => e.title);
    expect(titlesOf("deploy/node/web").some((title) => productTitles.includes(title))).toBe(
      false,
    );
    expect(titlesOf("sdk/node/web")).toEqual(expect.arrayContaining(productTitles));
  });

  it("asks the deploy cell for health, static and the probes, and nothing else", () => {
    expect(deploy.rows).toEqual([...healthRows, ...staticRows, ...probeRows]);
  });

  it("leaves the env row to the sdk cell, since defineEnv is what delivers it", () => {
    expect(deploy.rows.map((row) => row.title)).not.toContain(ENV_ROW);
    expect(sdk.rows.map((row) => row.title)).toContain(ENV_ROW);
  });

  it("leaves redeploy and rollback out of both, since neither observes a replacement", () => {
    for (const cell of ["deploy/node/web", "sdk/node/web"]) {
      const titles = planned.filter((entry) => entry.cell === cell).map((entry) => entry.title);
      expect(titles).not.toContain(REDEPLOY_TITLE);
      expect(titles).not.toContain(ROLLBACK_TITLE);
      expect(titles.some((title) => title.startsWith("redeploy · "))).toBe(false);
      expect(titles.some((title) => title.startsWith("rollback · "))).toBe(false);
    }
  });
});

describe("planning the legs a fixture asks for", () => {
  const lifecycle = specByName("lifecycle", "next");

  it("runs redeploy and rollback with the contract after each, where the fixture lives", () => {
    const cells = planTests([base(lifecycle)], LIVES).filter(
      (entry) => entry.cell === "lifecycle/next/web",
    );
    const titles = cells.map((entry) => entry.title);
    expect(titles).toContain(REDEPLOY_TITLE);
    expect(titles).toContain(ROLLBACK_TITLE);
    for (const leg of ["redeploy", "rollback"] as const) {
      const rows = cells.filter((entry) => entry.leg === leg && entry.title.includes(" · "));
      expect(rows.map((entry) => entry.title)).toEqual(
        lifecycle.rows.map((row) => `${leg} · ${row.title}`),
      );
    }
  });

  it("plans neither where the target cannot replace a release", () => {
    const titles = planTests([base(lifecycle)], SERVES).map((entry) => entry.title);
    expect(titles).not.toContain(REDEPLOY_TITLE);
    expect(titles).not.toContain(ROLLBACK_TITLE);
    expect(titles).toContain(UP_TITLE);
    expect(titles).toContain(DESTROY_TITLE);
  });
});

function withHooks(rows: LadderRow[], refuse: boolean): FixtureSpec {
  return {
    name: "with-sst",
    concern: "sdk",
    dir: "sdk/with-sst",
    runtime: "node",
    kind: "ladder",
    rows: healthRows,
    apps: ["web"],
    legs: LIVES,
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
  it("plans nothing extra for a fixture with no hooks", () => {
    const composite: FixtureSpec = {
      name: "node",
      concern: "deploy",
      dir: "deploy/node",
      runtime: "node",
      kind: "composite",
      rows: healthRows,
      apps: ["web"],
      legs: SERVES,
    };
    const planned = planTests([base(composite)], [...LIVES]);
    expect(planned.some((row) => row.title === REFUSE_TITLE)).toBe(false);
    expect(planned.some((row) => row.title.startsWith("publish"))).toBe(false);
  });

  it("plans refuse once, before anything else, for a hooked ladder", () => {
    const fixture = withHooks([publishRow], true);
    const planned = planTests([base(fixture)], [...LIVES]);
    const titles = planned
      .filter((row) => row.cell === cellKey("sdk/with-sst", "web"))
      .map((row) => row.title);
    expect(titles.filter((title) => title === REFUSE_TITLE).length).toBe(1);
    expect(titles.indexOf(REFUSE_TITLE)).toBeLessThan(titles.indexOf("publish · lists both records"));
  });

  it("plans one publish, outlive and prune title but three consume titles", () => {
    const fixture = withHooks([publishRow, consumeRow, outliveRow, pruneRow], true);
    const planned = planTests([base(fixture)], [...LIVES]).map((row) => row.title);
    expect(planned).toContain("publish · lists both records");
    expect(planned).toContain("outlive · the record survives");
    expect(planned).toContain("prune · both partitions are empty");
    expect(planned).toContain("consume · both link routes answer");
    expect(planned).toContain("redeploy · consume · both link routes answer");
    expect(planned).toContain("rollback · consume · both link routes answer");
    expect(planned.filter((title) => title.includes("both link routes answer")).length).toBe(3);
  });

  it("plans no refuse title when the fixture declares no refuse hook", () => {
    const fixture = withHooks([publishRow], false);
    const planned = planTests([base(fixture)], [...LIVES]);
    expect(planned.some((row) => row.title === REFUSE_TITLE)).toBe(false);
    expect(planned.some((row) => row.title === "publish · lists both records")).toBe(true);
  });
});

describe("planning the cells a fixture runs on a target", () => {
  const node = specByName("sdk", "node");

  function cellsOn(target: TargetName): string[] {
    return [...new Set(planTests(cellsOf(node, target), ["up"]).map((row) => row.cell))];
  }

  it("plans one cell per variant beside the base cell on aws", () => {
    expect(cellsOn("aws")).toEqual([
      "sdk/node/web",
      "sdk/node-container/web",
      "sdk/node-api-gateway/web",
      "sdk/node-cloudflare/web",
    ]);
  });

  it("plans only the variants a target runs", () => {
    expect(cellsOn("vps")).toEqual(["sdk/node/web"]);
    expect(cellsOn("dev")).toEqual(["sdk/node/web"]);
  });

  it("carries the fixture, app and variant of every test it plans", () => {
    const planned = planTests(cellsOf(specByName("sdk", "workspace"), "aws"), ["up"]);
    expect(planned.find((row) => row.cell === "sdk/workspace-container/express")).toEqual({
      cell: "sdk/workspace-container/express",
      fixture: "sdk/workspace",
      app: "express",
      variant: "container",
      title: UP_TITLE,
      leg: "up",
    });
    expect(planned.find((row) => row.cell === "sdk/workspace/next")?.variant).toBe("base");
  });
});
