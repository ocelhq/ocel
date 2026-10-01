import { describe, expect, it } from "bun:test";
import {
  exactTaskPayloadCheck,
  exactTopicPayloadCheck,
  hyphenatedTaskCheck,
  nextCacheChecks,
  nextDataCacheChecks,
  nextOriginCacheChecks,
  nextOriginDataCacheChecks,
  realtimeEventSizeCheck,
  realtimePublicCheck,
  realtimeReauthorizeCheck,
  realtimeRelayedPublishCheck,
  realtimeRuleAllowsCheck,
  realtimeWildcardCheck,
} from "../checks";
import { NO_FILTER, plan, type RunFilter } from "../plan";
import { filterFrom } from "../run/filter";
import { hasReleaseCycle, targetNamed } from "../targets";
import { fixtures } from "./fixtures";
import { gaps } from "./gaps";
import { type Concern, LANES, type Lane, targetOfLane } from "./types";

const REGISTRY_CREDENTIALS = {
  OCEL_JOURNEY_REGISTRY_USER: "octocat",
  OCEL_JOURNEY_REGISTRY_TOKEN: "ghs_t0ken",
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
      expect(concernIn("iac", planOn(lane, {}, filterFrom({ OCEL_JOURNEY_SKIPS: "run" })))).toEqual(
        [],
      );
    }
  });

  it("plans no iac cell for a pull request whose diff touches the iac fixtures", () => {
    const touched = {
      OCEL_JOURNEY_SEED: "42",
      OCEL_JOURNEY_TOUCHED: "iac/with-sst,iac/with-pulumi",
    };
    for (const lane of IAC_LANES) {
      expect(concernIn("iac", planOn(lane, {}, filterFrom(touched)))).toEqual([]);
      expect(
        concernIn("iac", planOn(lane, {}, filterFrom({ ...touched, OCEL_JOURNEY_SKIPS: "run" }))),
      ).toEqual([]);
    }
  });

  it("plans the iac cells a run names the concern for", () => {
    for (const lane of IAC_LANES) {
      const planned = planOn(
        lane,
        {},
        filterFrom({ OCEL_JOURNEY_CONCERN: "iac", OCEL_JOURNEY_SKIPS: "run" }),
      );
      expect(planned.cells.map((cell) => cell.fixture)).toContain("iac/with-sst");
      expect(planned.cells.map((cell) => cell.fixture)).toContain("iac/with-pulumi");
    }
  });

  it("refuses an iac fixture named under a run that does not name iac", () => {
    for (const concern of [undefined, "sdk", "deploy lifecycle sdk"]) {
      const filter = filterFrom({
        OCEL_JOURNEY_CONCERN: concern,
        OCEL_JOURNEY_FIXTURES: "iac/with-sst",
      });
      expect(() => planOn("aws", {}, filter)).toThrow(/no fixture named iac\/with-sst/);
    }
  });
});

describe("a pull request's run", () => {
  it("draws sdk cells when it names no concern", () => {
    const drawn = filterFrom({ OCEL_JOURNEY_SEED: "42", OCEL_JOURNEY_TOUCHED: "" });
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
    OCEL_JOURNEY_ZONE: "journeys.example.com",
    CLOUDFLARE_API_TOKEN: "token",
    CLOUDFLARE_ACCOUNT_ID: "account",
  };
  const titlesOf = (planned: ReturnType<typeof planOn>, cell: string) =>
    planned.cells.find((one) => one.name === cell)?.steps.map((one) => one.title) ?? [];
  const cacheTitlesIn = (titles: string[], of: string[]) =>
    titles.filter((title) => of.some((cache) => title.endsWith(cache)));

  it("holds a Next app on a box with no edge to the Next server's own cache, green but for the data cache, whatever zone the run names", () => {
    const zones = [{}, { OCEL_JOURNEY_ZONE: "journeys.example.com" }];
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
    const { OCEL_JOURNEY_REGISTRY_USER: _user, ...tokenOnly } = REGISTRY_CREDENTIALS;
    const { OCEL_JOURNEY_REGISTRY_TOKEN: _token, ...userOnly } = REGISTRY_CREDENTIALS;
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

  it("runs the behavioural suite on a box, with only the TypeScript SDK's payload checks red on tasks/node", () => {
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
      expect(issuesOf("tasks/go/web")).toEqual({});
    }
  });

  it("skips the suite on aws and gcp with the provider's refusal, under each target's ticket", () => {
    const tickets = {
      aws: 1469,
      "aws.floci": 1469,
      gcp: 1470,
      "gcp.floci": 1470,
    } as const;
    for (const [lane, issue] of Object.entries(tickets) as [Lane, number][]) {
      const planned = planOn(lane);
      const skipped = Object.keys(planned.skipped).filter((cell) => cell.startsWith("tasks/"));
      expect(skipped).toEqual(["tasks/node", "tasks/go"]);
      for (const cell of skipped) {
        const refusal = planned.skipped[cell]?.find((gap) => gap.issue === issue);
        expect(refusal?.reason).toMatch(/topics, tasks and workers are unsupported/);
      }
      expect(planOn(lane, {}, EVERY_CELL).cells.map((cell) => cell.name)).toContain("tasks/node");
    }
  });
});

describe("the realtime concern", () => {
  const EVERY_CELL = { ...NO_FILTER, runSkipped: true };
  const CELLS = ["realtime/node", "realtime/go"];

  it("runs the behavioural suite on dev in TypeScript and Go, expecting red only what needs a server publish to reach the gateway", () => {
    const planned = planOn("dev");
    expect(planned.cells.map((cell) => cell.name)).toEqual(expect.arrayContaining(CELLS));
    const needsServerPublish = Object.fromEntries(
      [
        realtimeRuleAllowsCheck,
        realtimePublicCheck,
        realtimeWildcardCheck,
        realtimeRelayedPublishCheck,
        realtimeReauthorizeCheck,
        realtimeEventSizeCheck,
      ].map((one) => [one.title, [1540]]),
    );
    for (const cell of CELLS) {
      const listed = Object.entries(planned.expectedFailures[`${cell}/web`] ?? {}).map(
        ([title, gaps]) => [title, gaps.map((gap) => gap.issue)],
      );
      expect(Object.fromEntries(listed)).toEqual(needsServerPublish);
    }
  });

  it("skips the suite on aws, gcp and a box with the provider's refusal, under each target's ticket", () => {
    const tickets = {
      aws: 1514,
      "aws.floci": 1514,
      gcp: 1515,
      "gcp.floci": 1515,
      vps: 1516,
      "vps.incus": 1516,
    } as const;
    for (const [lane, issue] of Object.entries(tickets) as [Lane, number][]) {
      const planned = planOn(lane);
      const skipped = Object.keys(planned.skipped).filter((cell) => cell.startsWith("realtime/"));
      expect(skipped).toEqual(CELLS);
      for (const cell of CELLS) {
        const refusal = planned.skipped[cell]?.find((gap) => gap.issue === issue);
        expect(refusal?.reason).toMatch(/realtime is unsupported/);
      }
      expect(planOn(lane, {}, EVERY_CELL).cells.map((cell) => cell.name)).toEqual(
        expect.arrayContaining(CELLS),
      );
    }
  });
});
