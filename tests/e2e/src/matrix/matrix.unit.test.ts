import { describe, expect, it } from "bun:test";
import { existsSync, readFileSync } from "node:fs";
import path from "node:path";
import stripJsonComments from "strip-json-comments";
import {
  batchCheck,
  exactTaskPayloadCheck,
  exactTopicPayloadCheck,
  hyphenatedTaskCheck,
  lanesCheck,
  nextCacheChecks,
  nextDataCacheChecks,
  nextOriginCacheChecks,
  nextOriginDataCacheChecks,
  orderedKeysInParallelCheck,
  realtimeChecks,
  realtimeOperationLimitCheck,
  realtimeRuleDeniesCheck,
  taskConcurrencyCheck,
} from "../checks";
import { DEFAULT_BASE, GCP_BASE, VPS_BASE } from "../config";
import { fixtureDir } from "../paths";
import { NO_FILTER, plan, type RunFilter } from "../plan";
import { filterFrom } from "../run/filter";
import { hasReleaseCycle, targetNamed } from "../targets";
import { fixtures } from "./fixtures";
import { gaps } from "./gaps";
import { type Concern, LANES, type Lane, type TargetName, targetOfLane } from "./types";

const REGISTRY_CREDENTIALS = {
  OCEL_E2E_REGISTRY_USER: "octocat",
  OCEL_E2E_REGISTRY_TOKEN: "ghs_t0ken",
};

function planOn(lane: Lane, env: NodeJS.ProcessEnv = {}, filter: RunFilter = NO_FILTER) {
  const releaseCycle = hasReleaseCycle(targetNamed(targetOfLane(lane)));
  return plan({ fixtures, gaps, lane, releaseCycle, filter, env });
}

function concernIn(concern: Concern, planned: ReturnType<typeof planOn>): string[] {
  return [...planned.cells.map((cell) => cell.name), ...Object.keys(planned.skipped)].filter(
    (name) => name.startsWith(`${concern}/`),
  );
}

describe("the journey matrix", () => {
  it("plans on every lane without a dead gap, with the registry credentials set and without", () => {
    for (const lane of LANES) {
      expect(() => planOn(lane)).not.toThrow();
      expect(() => planOn(lane, REGISTRY_CREDENTIALS)).not.toThrow();
    }
  });

  it("files the fixtures an SST or Pulumi stack deploys under iac, and only those", () => {
    const named = (concern: Concern) => fixtures.filter((one) => one.concern === concern);
    const stacked = fixtures.filter((one) => one.stack !== undefined);
    expect(named("sdk").filter((one) => one.stack !== undefined)).toEqual([]);
    expect(named("iac")).toEqual(stacked);
    expect(stacked).not.toEqual([]);
  });
});

describe("the iac concern", () => {
  const IAC_LANES = ["aws", "aws.floci"] as const;

  it("plans no iac cell for a run that leaves the concern unnamed", () => {
    for (const lane of IAC_LANES) {
      expect(concernIn("iac", planOn(lane, {}, { ...NO_FILTER, runSkipped: true }))).toEqual([]);
      expect(concernIn("iac", planOn(lane, {}, filterFrom({ OCEL_E2E_SKIPS: "run" })))).toEqual([]);
    }
  });

  it("plans no iac cell for a pull request whose diff touches the iac fixtures", () => {
    const touched = {
      OCEL_E2E_SEED: "42",
      OCEL_E2E_TOUCHED: "iac/with-sst,iac/with-pulumi",
    };
    for (const lane of IAC_LANES) {
      expect(concernIn("iac", planOn(lane, {}, filterFrom(touched)))).toEqual([]);
      expect(
        concernIn("iac", planOn(lane, {}, filterFrom({ ...touched, OCEL_E2E_SKIPS: "run" }))),
      ).toEqual([]);
    }
  });

  it("plans the iac cells a run names the concern for", () => {
    for (const lane of IAC_LANES) {
      const planned = planOn(
        lane,
        {},
        filterFrom({ OCEL_E2E_CONCERN: "iac", OCEL_E2E_SKIPS: "run" }),
      );
      expect(planned.cells.map((cell) => cell.fixture)).toContain("iac/with-sst");
      expect(planned.cells.map((cell) => cell.fixture)).toContain("iac/with-pulumi");
    }
  });

  it("refuses an iac fixture named under a run that does not name iac", () => {
    for (const concern of [undefined, "sdk", "deploy lifecycle sdk"]) {
      const filter = filterFrom({
        OCEL_E2E_CONCERN: concern,
        OCEL_E2E_FIXTURES: "iac/with-sst",
      });
      expect(() => planOn("aws", {}, filter)).toThrow(/no fixture named iac\/with-sst/);
    }
  });
});

describe("a pull request's run", () => {
  it("draws sdk cells when it names no concern", () => {
    const drawn = filterFrom({ OCEL_E2E_SEED: "42", OCEL_E2E_TOUCHED: "" });
    for (const lane of ["aws.floci", "dev", "vps.incus"] as const) {
      expect(concernIn("sdk", planOn(lane, {}, drawn))).not.toEqual([]);
    }
  });
});

describe("the Next cache a cell is held to", () => {
  const EDGE_TITLES = [...nextCacheChecks, ...nextDataCacheChecks].map((one) => one.title);
  const ORIGIN_TITLES = [...nextOriginCacheChecks, ...nextOriginDataCacheChecks].map(
    (one) => one.title,
  );
  const DATA_CACHE_TITLES = nextOriginDataCacheChecks.map((one) => one.title);
  const EVERY_CELL = { ...NO_FILTER, runSkipped: true };
  const ZONED = {
    OCEL_E2E_ZONE: "journeys.example.com",
    CLOUDFLARE_API_TOKEN: "token",
    CLOUDFLARE_ACCOUNT_ID: "account",
  };
  const titlesOf = (planned: ReturnType<typeof planOn>, cell: string) =>
    planned.cells.find((one) => one.name === cell)?.steps.map((one) => one.title) ?? [];
  const cacheTitlesIn = (titles: string[], of: string[]) =>
    titles.filter((title) => of.some((cache) => title.endsWith(cache)));

  it("holds a Next app on a box with no edge to the Next server's own cache, green but for the data cache, whatever zone the run names", () => {
    const zones = [{}, { OCEL_E2E_ZONE: "journeys.example.com" }];
    for (const lane of ["vps", "vps.incus"] as const) {
      for (const env of zones) {
        const planned = planOn(lane, env, EVERY_CELL);
        const red = DATA_CACHE_TITLES;
        for (const cell of ["deploy/next", "sdk/next", "lifecycle/next"]) {
          const titles = titlesOf(planned, cell);
          expect(cacheTitlesIn(titles, EDGE_TITLES)).toEqual([]);
          expect(cacheTitlesIn(titles, ORIGIN_TITLES)).not.toEqual([]);
          const listed = planned.expectedFailures[`${cell}/web`] ?? {};
          expect(cacheTitlesIn(Object.keys(listed), ORIGIN_TITLES)).toEqual(
            cacheTitlesIn(titles, red),
          );
          for (const title of cacheTitlesIn(titles, red)) {
            expect(listed[title]?.map((gap) => gap.issue)).toEqual([1458]);
          }
        }
      }
    }
  });

  it("holds a Next app Cloudflare fronts on a box to the edge's cache, listed red under #1457", () => {
    for (const lane of ["vps", "vps.incus"] as const) {
      const planned = planOn(lane, ZONED, EVERY_CELL);
      for (const cell of ["lifecycle/next-cloudflare", "lifecycle/next-cloudflare-tunnel"]) {
        const titles = titlesOf(planned, cell);
        expect(cacheTitlesIn(titles, ORIGIN_TITLES)).toEqual([]);
        const cached = cacheTitlesIn(titles, EDGE_TITLES);
        expect(cached).not.toEqual([]);
        for (const title of cached) {
          expect(
            planned.expectedFailures[`${cell}/web`]?.[title]?.map((gap) => gap.issue),
          ).toContain(1457);
        }
      }
    }
  });

  it("holds a Next app in a container on aws or gcp to the Next server's own cache", () => {
    const containers = {
      aws: ["deploy/next-container", "lifecycle/next-container", "sdk/next-container"],
      gcp: ["deploy/next-container"],
    } as const;
    for (const [lane, cells] of Object.entries(containers) as [Lane, readonly string[]][]) {
      const planned = planOn(lane, {}, EVERY_CELL);
      for (const cell of cells) {
        const titles = titlesOf(planned, cell);
        expect(cacheTitlesIn(titles, EDGE_TITLES)).toEqual([]);
        expect(cacheTitlesIn(titles, ORIGIN_TITLES)).not.toEqual([]);
      }
    }
  });

  it("holds a Next app on aws to the edge's cache", () => {
    const planned = planOn("aws", {}, EVERY_CELL);
    for (const cell of ["deploy/next", "deploy/next-cloudflare", "lifecycle/next"]) {
      const titles = titlesOf(planned, cell);
      expect(cacheTitlesIn(titles, ORIGIN_TITLES)).toEqual([]);
      expect(cacheTitlesIn(titles, EDGE_TITLES)).not.toEqual([]);
    }
  });
});

describe("the registry variant", () => {
  it("deploys through the registry on either box when the run has its credentials", () => {
    for (const lane of ["vps", "vps.incus"] as const) {
      expect(planOn(lane, REGISTRY_CREDENTIALS).cells.map((cell) => cell.name)).toContain(
        "deploy/node-registry",
      );
    }
  });

  it("is skipped on either box when the run lacks the user or the token it pushes with", () => {
    const { OCEL_E2E_REGISTRY_USER: _user, ...tokenOnly } = REGISTRY_CREDENTIALS;
    const { OCEL_E2E_REGISTRY_TOKEN: _token, ...userOnly } = REGISTRY_CREDENTIALS;
    for (const lane of ["vps", "vps.incus"] as const) {
      for (const env of [{}, tokenOnly, userOnly]) {
        const planned = planOn(lane, env);
        expect(planned.cells.map((cell) => cell.name)).not.toContain("deploy/node-registry");
        expect(planned.skipped["deploy/node-registry"]?.map((gap) => gap.id)).toEqual([
          "no-registry-credentials",
        ]);
      }
    }
  });
});

describe("the kv concern", () => {
  const EVERY_CELL = { ...NO_FILTER, runSkipped: true };

  it("runs the behavioural suite on dev, on either box and on real gcp", () => {
    for (const lane of ["dev", "vps", "vps.incus", "gcp"] as const) {
      expect(planOn(lane, {}, EVERY_CELL).cells.map((cell) => cell.name)).toContain("kv/node");
    }
  });

  it("runs the behavioural suite in a container on aws", () => {
    expect(planOn("aws", {}, EVERY_CELL).cells.map((cell) => cell.name)).toContain(
      "kv/node-container",
    );
    const skippedBy = planOn("aws").skipped["kv/node-container"] ?? [];
    expect(skippedBy.filter((gap) => /kv/.test(gap.reason))).toEqual([]);
  });

  it("skips the suite on floci's aws, which runs no container behind a load balancer", () => {
    const planned = planOn("aws.floci");
    expect(planned.skipped["kv/node-container"]?.map((gap) => gap.issue)).toEqual([995]);
  });

  it("skips the suite on floci's gcp, which serves no Memorystore", () => {
    const planned = planOn("gcp.floci");
    expect(planned.skipped["kv/node"]?.map((gap) => gap.id)).toEqual([
      "floci-serves-no-memorystore",
    ]);
  });
});

describe("the tasks concern", () => {
  const EVERY_CELL = { ...NO_FILTER, runSkipped: true };

  it("runs the behavioural suite and the wire checks on dev", () => {
    const planned = planOn("dev").cells.map((cell) => cell.name);
    expect(planned).toContain("tasks/node");
    expect(planned).toContain("tasks/go");
  });

  it("expects only the payload checks red on tasks/node on dev, under the TypeScript SDK's ticket, and nothing red on tasks/go", () => {
    const { expectedFailures } = planOn("dev");
    const issuesOf = (cell: string) =>
      Object.fromEntries(
        Object.entries(expectedFailures[cell] ?? {}).map(([title, listed]) => [
          title,
          listed.map((gap) => gap.issue),
        ]),
      );
    expect(issuesOf("tasks/node/web")).toEqual({
      [hyphenatedTaskCheck.title]: [1526],
      [exactTaskPayloadCheck.title]: [1528],
      [exactTopicPayloadCheck.title]: [1528],
    });
    expect(issuesOf("tasks/go/web")).toEqual({});
  });

  it("runs the behavioural suite on a box, with the TypeScript SDK's payload checks red on tasks/node and tasks/go's image red under its own ticket", () => {
    for (const lane of ["vps", "vps.incus"] as const) {
      const planned = planOn(lane, {}, EVERY_CELL);
      const names = planned.cells.map((cell) => cell.name);
      expect(names).toContain("tasks/node");
      expect(names).toContain("tasks/go");
      expect(Object.keys(planned.skipped).filter((cell) => cell.startsWith("tasks/"))).toEqual([]);
      const issuesOf = (cell: string) =>
        Object.fromEntries(
          Object.entries(planned.expectedFailures[cell] ?? {}).map(([title, listed]) => [
            title,
            listed.map((gap) => gap.issue),
          ]),
        );
      expect(issuesOf("tasks/node/web")).toEqual({
        [exactTaskPayloadCheck.title]: [1528],
        [exactTopicPayloadCheck.title]: [1528],
      });
      expect(issuesOf("tasks/go/web")).toEqual({ deploy: [1550] });
    }
  });

  it("runs the behavioural suite on gcp, with lanes, batches, task concurrency and the TypeScript SDK's payload checks red on tasks/node", () => {
    for (const lane of ["gcp", "gcp.floci"] as const) {
      const planned = planOn(lane);
      expect(Object.keys(planned.skipped).filter((cell) => cell.startsWith("tasks/"))).toEqual([]);
      expect(planned.cells.map((cell) => cell.name)).toEqual(
        expect.arrayContaining(["tasks/node", "tasks/go"]),
      );
      const issuesOf = (cell: string) =>
        Object.fromEntries(
          Object.entries(planned.expectedFailures[cell] ?? {}).map(([title, listed]) => [
            title,
            listed.map((gap) => gap.issue),
          ]),
        );
      expect(issuesOf("tasks/node/web")).toEqual({
        [lanesCheck.title]: [1562],
        [batchCheck.title]: [1563],
        [taskConcurrencyCheck.title]: [1564],
        [exactTaskPayloadCheck.title]: [1528],
        [exactTopicPayloadCheck.title]: [1528],
      });
      expect(issuesOf("tasks/go/web")).toEqual({});
    }
  });

  it("runs the suite on aws behind api gateway, expecting lanes red there and floci's serial delivery red on floci", () => {
    const red = (lane: Lane) =>
      Object.keys(
        planOn(lane, {}, EVERY_CELL).expectedFailures["tasks/node-api-gateway/web"] ?? {},
      ).sort();
    const payloads = [exactTaskPayloadCheck.title, exactTopicPayloadCheck.title];
    expect(red("aws")).toEqual([lanesCheck.title, ...payloads].sort());
    expect(red("aws.floci")).toEqual(
      [
        lanesCheck.title,
        orderedKeysInParallelCheck.title,
        taskConcurrencyCheck.title,
        ...payloads,
      ].sort(),
    );
    for (const lane of ["aws", "aws.floci"] as const) {
      expect(Object.keys(planOn(lane).skipped).filter((cell) => cell.startsWith("tasks/"))).toEqual(
        [],
      );
    }
  });
});

describe("the realtime concern", () => {
  const EVERY_CELL = { ...NO_FILTER, runSkipped: true };
  const CELLS = ["realtime/node", "realtime/go", "realtime/python", "realtime/rust"];

  it("runs the behavioural suite on dev in TypeScript, Go, Python and Rust, expecting nothing red", () => {
    const planned = planOn("dev");
    expect(planned.cells.map((cell) => cell.name)).toEqual(expect.arrayContaining(CELLS));
    for (const cell of CELLS) {
      expect(planned.expectedFailures[`${cell}/web`] ?? {}).toEqual({});
    }
  });

  it("runs the behavioural suite on aws in every language, expecting nothing red", () => {
    const planned = planOn("aws");
    expect(planned.cells.map((cell) => cell.name)).toEqual(expect.arrayContaining(CELLS));
    for (const cell of CELLS) {
      expect(planned.skipped[cell]).toBeUndefined();
      expect(planned.expectedFailures[`${cell}/web`] ?? {}).toEqual({});
    }
  });

  it("skips the suite on floci, which runs no AppSync Event API", () => {
    const planned = planOn("aws.floci");
    expect(Object.keys(planned.skipped).filter((cell) => cell.startsWith("realtime/"))).toEqual(
      CELLS,
    );
    for (const cell of CELLS) {
      const gap = planned.skipped[cell]?.find((one) => one.id === "floci-runs-no-event-api");
      expect(gap?.reason).toMatch(/Event API/);
    }
  });

  it("runs the behavioural suite on a box in TypeScript, and skips Go, Python and Rust under the tickets for their images", () => {
    for (const lane of ["vps", "vps.incus"] as const) {
      const planned = planOn(lane);
      expect(planned.cells.map((cell) => cell.name)).toContain("realtime/node");
      expect(planned.expectedFailures["realtime/node/web"] ?? {}).toEqual({});
      expect(
        Object.fromEntries(
          Object.entries(planned.skipped)
            .filter(([cell]) => cell.startsWith("realtime/"))
            .map(([cell, gaps]) => [cell, gaps.map((gap) => gap.issue)]),
        ),
      ).toEqual({
        "realtime/go": [1550],
        "realtime/python": [1598],
        "realtime/rust": [1598],
      });
      expect(planOn(lane, {}, EVERY_CELL).cells.map((cell) => cell.name)).toEqual(
        expect.arrayContaining(CELLS),
      );
    }
  });

  const GCP_SKIPS: Record<string, number> = { "realtime/python": 1260, "realtime/rust": 1609 };
  const GCP_CELLS = CELLS.filter((cell) => !(cell in GCP_SKIPS));

  it("runs the behavioural suite on gcp in TypeScript and Go, expecting nothing red, and skips Python and Rust under the tickets for their builds", () => {
    for (const lane of ["gcp", "gcp.floci"] as const) {
      const planned = planOn(lane);
      expect(planned.cells.map((cell) => cell.name)).toEqual(expect.arrayContaining(GCP_CELLS));
      for (const [cell, issue] of Object.entries(GCP_SKIPS)) {
        expect(planned.skipped[cell]?.map((gap) => gap.issue)).toEqual([issue]);
      }
    }
    const planned = planOn("gcp");
    for (const cell of GCP_CELLS) {
      expect(planned.skipped[cell]).toBeUndefined();
      expect(planned.expectedFailures[`${cell}/web`] ?? {}).toEqual({});
    }
  });

  it("runs the suite on floci-gcp, expecting red every check that holds a socket or hears a publish, or asks the handler to know its own origin", () => {
    const planned = planOn("gcp.floci");
    const handlerOnly = [realtimeRuleDeniesCheck, realtimeOperationLimitCheck].map(
      (one) => one.title,
    );
    for (const cell of GCP_CELLS) {
      expect(planned.skipped[cell]).toBeUndefined();
      const red = Object.keys(planned.expectedFailures[`${cell}/web`] ?? {});
      expect(red.sort()).toEqual(
        realtimeChecks
          .map((one) => one.title)
          .filter((title) => !handlerOnly.includes(title))
          .sort(),
      );
    }
  });
});

describe("the fixtures a lane deploys", () => {
  const BASES: Record<Exclude<TargetName, "dev">, string> = {
    aws: DEFAULT_BASE,
    gcp: GCP_BASE,
    vps: VPS_BASE,
  };
  const MARKERS = ["go.mod", "pyproject.toml", "requirements.txt", "package.json", "Cargo.toml"];

  it("put every app a planned cell deploys where ocel can tell what it is built with", () => {
    const undetectable = new Set<string>();
    for (const lane of LANES) {
      const target = targetOfLane(lane);
      if (target === "dev") {
        continue;
      }
      for (const cell of planOn(lane).cells) {
        const dir = fixtureDir(cell.fixture);
        const base = path.join(dir, BASES[target]);
        if (!existsSync(base)) {
          continue;
        }
        const config = JSON.parse(stripJsonComments(readFileSync(base, "utf8"))) as {
          apps?: { name: string; path: string; framework?: string }[];
        };
        for (const app of config.apps ?? []) {
          const appDir = path.join(dir, app.path);
          if (!app.framework && !MARKERS.some((marker) => existsSync(path.join(appDir, marker)))) {
            undetectable.add(`${lane}: ${cell.fixture} ${BASES[target]} app ${app.name}`);
          }
        }
      }
    }
    expect([...undetectable].sort()).toEqual([]);
  });
});
