import { describe, expect, it } from "bun:test";
import { existsSync, readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import stripJsonComments from "strip-json-comments";
import {
  batchCheck,
  bindingQueryCheck,
  buildVariablesChecks,
  clientEnvDeploymentUrlCheck,
  imageShowsNoBindingCheck,
  lanesCheck,
  nextCacheChecks,
  nextClientEnvChecks,
  nextDataCacheChecks,
  nextOriginCacheChecks,
  nextOriginDataCacheChecks,
  nextPprChecks,
  nextStoreReadChecks,
  orderedKeysInParallelCheck,
  preBuildChecks,
  preBuildPrerenderChecks,
  prerenderBucketChecks,
  prerenderChecks,
  taskConcurrencyCheck,
  tasksWireChecks,
  todoAndDocumentChecks,
} from "../checks";
import { PREVIEW_TITLES } from "../checks/previews";
import { JSON_BASE } from "../config";
import { fixtureDir } from "../paths";
import { NO_FILTER, plan, type RunFilter } from "../plan";
import { filterFrom } from "../run/filter";
import { hasPreviewGate, hasReleaseCycle, previewsOn, targetNamed } from "../targets";
import { fixtures } from "./fixtures";
import { gaps } from "./gaps";
import { CONCERNS, type Concern, LANES, type Lane, targetOfLane } from "./types";

const REGISTRY_CREDENTIALS = {
  OCEL_E2E_REGISTRY_USER: "octocat",
  OCEL_E2E_REGISTRY_TOKEN: "ghs_t0ken",
};

function planOn(lane: Lane, env: NodeJS.ProcessEnv = {}, filter: RunFilter = NO_FILTER) {
  const target = targetNamed(targetOfLane(lane));
  const releaseCycle = hasReleaseCycle(target);
  return plan({
    fixtures,
    gaps,
    lane,
    releaseCycle,
    previews: previewsOn(target, lane),
    gatesPreviews: hasPreviewGate(target),
    filter,
    env,
  });
}

function concernIn(concern: Concern, planned: ReturnType<typeof planOn>): string[] {
  return [...planned.cells.map((cell) => cell.name), ...Object.keys(planned.skipped)].filter(
    (name) => name.startsWith(`${concern}/`),
  );
}

describe("the fixtures on disk", () => {
  const root = path.dirname(fixtureDir("deploy"));
  const dirsIn = (dir: string) =>
    readdirSync(dir, { withFileTypes: true })
      .filter((entry) => entry.isDirectory())
      .map((entry) => entry.name)
      .sort();

  it("are one directory per concern and nothing else", () => {
    expect(dirsIn(root)).toEqual([...CONCERNS].sort());
  });

  it("are exactly the apps the matrix drives, each under its concern", () => {
    for (const concern of CONCERNS) {
      const driven = fixtures
        .filter((one) => one.concern === concern)
        .map((one) => one.name.split("/")[1])
        .sort();
      const tracked = dirsIn(path.join(root, concern)).filter(
        (name) =>
          existsSync(path.join(root, concern, name, "README.md")) ||
          readdirSync(path.join(root, concern, name)).some((file) => file.startsWith("ocel.")),
      );
      expect(tracked, concern).toEqual(driven);
    }
  });
});

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

const ZONED_RUN = {
  OCEL_E2E_ZONE: "journeys.example.com",
  CLOUDFLARE_API_TOKEN: "token",
  CLOUDFLARE_ACCOUNT_ID: "account",
};

const skippedBy = (planned: ReturnType<typeof planOn>, cell: string) =>
  (planned.skipped[cell] ?? []).map((gap) => gap.id).sort();

describe("the cloudflare variant on floci's aws", () => {
  it("skips every cloudflare cell, whatever framework its fixture builds", () => {
    expect(skippedBy(planOn("aws.floci"), "deploy/sveltekit-cloudflare")).toContain(
      "no-cloudflare-api",
    );
  });
});

describe("the alb variant", () => {
  it("runs deploy/node behind the load balancer on gcp when the run names a zone", () => {
    const planned = planOn("gcp", ZONED_RUN);

    expect(planned.cells.map((cell) => cell.name)).toContain("deploy/node-alb");
    expect(skippedBy(planned, "deploy/node-alb")).toEqual([]);
  });

  it("skips every alb cell on gcp when the run names no zone", () => {
    const planned = planOn("gcp");

    for (const cell of ["deploy/node-alb", "deploy/next-alb"]) {
      expect(skippedBy(planned, cell)).toContain("no-cloudflare-zone");
    }
  });

  it("skips every alb cell on floci-gcp, where nothing emulates the Cloudflare zone its hostnames live in", () => {
    const planned = planOn("gcp.floci", ZONED_RUN);

    for (const cell of ["deploy/node-alb", "deploy/next-alb"]) {
      expect(skippedBy(planned, cell)).toContain("no-cloudflare-api");
    }
  });

  it("plans on every lane with a zone named, without a dead gap", () => {
    for (const lane of LANES) {
      expect(() => planOn(lane, ZONED_RUN)).not.toThrow();
    }
  });
});

describe("Cloudflare in front of gcp", () => {
  it("skips every Cloudflare cell on gcp until the run names a zone", () => {
    expect(skippedBy(planOn("gcp"), "deploy/node-cloudflare")).toEqual(["no-cloudflare-zone"]);
  });

  it("deploys every Cloudflare cell on gcp when the run names a zone", () => {
    const planned = planOn("gcp", ZONED_RUN);

    expect(planned.cells.map((cell) => cell.name)).toEqual(
      expect.arrayContaining(["deploy/node-cloudflare", "deploy/next-cloudflare"]),
    );
    expect(skippedBy(planned, "deploy/node-cloudflare")).toEqual([]);
  });
});

describe("the needs a Google Cloud origin does not serve", () => {
  const WAIVED = {
    "deploy/next": ["edge-runtime", "edge-cache"],
    "lifecycle/next": ["edge-runtime", "edge-cache"],
    "sdk/next": ["edge-runtime", "edge-cache"],
    "deploy/workspace": ["edge-cache"],
    "sdk/workspace": ["edge-cache"],
  };

  it("waives the needs each Next fixture deploys degraded on gcp, since the origin serves them on node", () => {
    for (const [name, needs] of Object.entries(WAIVED)) {
      const waived: string[] =
        fixtures.find((one) => one.name === name)?.configOn?.gcp?.allowDegraded ?? [];
      expect([...waived].sort()).toEqual([...needs].sort());
    }
  });

  it("expects the edge runtime's stamp green on a Next container on gcp and floci-gcp, whose Next server runs edge routes itself", () => {
    for (const lane of ["gcp", "gcp.floci"] as const) {
      const listed = planOn(lane).expectedFailures["deploy/next-container/web"] ?? {};

      expect(
        Object.keys(listed).filter((title) => title.includes("stamp themselves apart")),
      ).toEqual([]);
    }
  });

  it("expects the edge runtime's stamp red on every Next deploy on gcp and floci-gcp, where edge routes run on node", () => {
    for (const lane of ["gcp", "gcp.floci"] as const) {
      const listed = planOn(lane).expectedFailures["deploy/next/web"] ?? {};
      const stamp = Object.entries(listed).filter(([title]) =>
        title.includes("stamp themselves apart"),
      );

      expect(stamp).not.toEqual([]);
      for (const [, gapsListed] of stamp) {
        expect(gapsListed.map((gap) => gap.id)).toContain(
          "google-cloud-origins-serve-no-edge-runtime",
        );
      }
    }
  });
});

describe("sdk/node", () => {
  const EVERY_CELL = { ...NO_FILTER, runSkipped: true };
  const issuesOf = (planned: ReturnType<typeof planOn>, cell: string) =>
    Object.fromEntries(
      Object.entries(planned.expectedFailures[cell] ?? {}).map(([title, listed]) => [
        title,
        listed.map((gap) => gap.issue),
      ]),
    );

  it("restarts what it declared and, where releases are kept, redeploys and rolls back", () => {
    const phasesOn = (lane: Lane) =>
      planOn(lane).cells.find((cell) => cell.name === "sdk/node")?.phases ?? [];
    expect(phasesOn("dev")).toEqual(["deploy", "verify", "restart", "destroy"]);
    expect(phasesOn("vps")).toEqual([
      "deploy",
      "verify",
      "restart",
      "redeploy",
      "rollback",
      "destroy",
    ]);
  });

  it("drives every primitive on dev and on either box, expecting nothing red", () => {
    for (const lane of ["dev", "vps", "vps.incus"] as const) {
      const planned = planOn(lane);
      expect(planned.cells.map((cell) => cell.name)).toContain("sdk/node");
      expect(issuesOf(planned, "sdk/node/web")).toEqual({});
    }
  });

  it("runs on gcp with its database checks red, since the runner cannot reach a private Cloud SQL instance, and lanes, batches and task concurrency red", () => {
    const planned = planOn("gcp");
    expect(planned.skipped["sdk/node"]).toBeUndefined();
    const red = planned.expectedFailures["sdk/node/web"] ?? {};
    for (const one of todoAndDocumentChecks) {
      expect(red[one.title]?.map((gap) => gap.id)).toEqual([
        "gcp-postgres-is-private-to-the-runner",
      ]);
    }
    expect(red[lanesCheck.title]?.map((gap) => gap.issue)).toEqual([1562]);
    expect(red[batchCheck.title]?.map((gap) => gap.issue)).toEqual([1563]);
    expect(red[taskConcurrencyCheck.title]?.map((gap) => gap.issue)).toEqual([1564]);
  });

  it("skips its cell on floci-gcp, which serves neither Memorystore nor Cloud SQL", () => {
    expect((planOn("gcp.floci").skipped["sdk/node"] ?? []).map((gap) => gap.id)).toEqual([
      "floci-serves-no-memorystore",
      "floci-serves-no-cloud-sql",
    ]);
  });

  it("runs every function cell on aws, whose functions join the VPC its postgres and kv answer in", () => {
    const planned = planOn("aws");
    for (const cell of ["sdk/node", "sdk/node-api-gateway"]) {
      expect(planned.skipped[cell]).toBeUndefined();
      expect(planned.expectedFailures[`${cell}/web`]?.[bindingQueryCheck.title]).toBeUndefined();
    }
  });

  it("skips every cell on floci's aws, which runs no AppSync Event API", () => {
    const skipped = planOn("aws.floci").skipped;
    for (const cell of ["sdk/node", "sdk/node-container", "sdk/node-api-gateway"]) {
      expect(skipped[cell]?.map((gap) => gap.id)).toContain("floci-runs-no-event-api");
    }
  });

  it("lists lanes red on aws behind api gateway, and floci's serial delivery on floci, should the cell run", () => {
    const red = (lane: Lane) => issuesOf(planOn(lane, {}, EVERY_CELL), "sdk/node-api-gateway/web");
    expect(red("aws")[lanesCheck.title]).toEqual([1559]);
    expect(red("aws.floci")[orderedKeysInParallelCheck.title]).toEqual([1558]);
    expect(red("aws.floci")[taskConcurrencyCheck.title]).toEqual([1558]);
  });
});

describe("previews on gcp", () => {
  const zoned = {
    OCEL_E2E_ZONE: "journeys.example.com",
    CLOUDFLARE_API_TOKEN: "token",
    CLOUDFLARE_ACCOUNT_ID: "account",
  };
  const previewTitlesOf = (planned: ReturnType<typeof planOn>, cell: string) =>
    (planned.cells.find((one) => one.name === cell)?.steps ?? [])
      .filter((one) => one.phase === "preview")
      .map((one) => one.title);

  it("previews deploy/next on gcp behind the load balancer when the run names a zone", () => {
    const planned = planOn("gcp", zoned);

    expect(previewTitlesOf(planned, "deploy/next")).toEqual([]);
    expect(previewTitlesOf(planned, "deploy/next-alb")).toEqual([
      PREVIEW_TITLES["own-release"],
      PREVIEW_TITLES.pruned,
    ]);
  });

  it("previews nothing on gcp when the run names no zone", () => {
    const planned = planOn("gcp");

    expect(previewTitlesOf(planned, "deploy/next")).toEqual([]);
    expect(planned.cells.map((cell) => cell.name)).not.toContain("deploy/next-alb");
  });

  it("plans no preview phase on floci-gcp", () => {
    const planned = planOn("gcp.floci", zoned, { ...NO_FILTER, runSkipped: true });

    for (const cell of planned.cells) {
      expect(cell.phases).not.toContain("preview");
    }
  });
});

describe("Next on gcp", () => {
  const EVERY_CELL = { ...NO_FILTER, runSkipped: true };
  const NEXT_DEPLOYS = [
    "deploy/next",
    "deploy/next-container",
    "deploy/workspace",
    "deploy/workspace-container",
  ];
  const POSTGRES_NEXT = ["lifecycle/next", "lifecycle/next-container", "sdk/next", "sdk/workspace"];
  const ids = (planned: ReturnType<typeof planOn>, cell: string) =>
    (planned.skipped[cell] ?? []).map((gap) => gap.id).sort();

  it("plans deploy/next and deploy/workspace on gcp and floci-gcp in either compute, with no gap left about Next on Cloud Run", () => {
    for (const lane of ["gcp", "gcp.floci"] as const) {
      const names = planOn(lane).cells.map((cell) => cell.name);
      for (const cell of NEXT_DEPLOYS) {
        expect(names).toContain(cell);
      }
    }
    expect(gaps.find((gap) => gap.id === "next-on-cloud-run")).toBeUndefined();
  });

  it("expects only floci's buffering and rewriting red on the Next deploys on floci-gcp, beyond what every gcp lane lists", () => {
    const planned = planOn("gcp.floci");
    const onEveryGcpLane = planOn("gcp").expectedFailures;
    const apps = { "deploy/workspace": ["next", "express"] } as Record<string, string[]>;
    const streamPage: Record<string, number> = { "deploy/next": 1, "deploy/next-container": 1 };
    for (const cell of NEXT_DEPLOYS) {
      for (const app of apps[cell.replace(/-container$/, "")] ?? ["web"]) {
        const listed = Object.fromEntries(
          Object.entries(planned.expectedFailures[`${cell}/${app}`] ?? {}).filter(
            ([title]) => !(title in (onEveryGcpLane[`${cell}/${app}`] ?? {})),
          ),
        );
        expect(Object.keys(listed)).toHaveLength(6 + (streamPage[cell] ?? 0));
        for (const gapsListed of Object.values(listed)) {
          expect(gapsListed.map((gap) => gap.id)).toEqual(["floci-cloud-run-buffers-and-rewrites"]);
        }
      }
    }
  });

  it("holds a serverless Next app on gcp with no edge to the Next server's own cache, since nothing caches in front of it", () => {
    const edgeTitles = [...nextCacheChecks, ...nextDataCacheChecks].map((one) => one.title);
    const originTitles = [...nextOriginCacheChecks, ...nextOriginDataCacheChecks].map(
      (one) => one.title,
    );
    const planned = planOn("gcp", {}, EVERY_CELL);
    for (const cell of ["deploy/next", "lifecycle/next"]) {
      const titles =
        planned.cells.find((one) => one.name === cell)?.steps.map((one) => one.title) ?? [];
      expect(titles.filter((title) => edgeTitles.some((cache) => title.endsWith(cache)))).toEqual(
        [],
      );
      expect(
        titles.filter((title) => originTitles.some((cache) => title.endsWith(cache))),
      ).not.toEqual([]);
    }
  });

  it("skips the Next fixtures that declare a postgres on floci-gcp, which serves no Cloud SQL", () => {
    const planned = planOn("gcp.floci");
    for (const cell of POSTGRES_NEXT) {
      expect(ids(planned, cell)).toEqual(["floci-serves-no-cloud-sql"]);
    }
  });

  it("deploys the Next fixtures that declare a postgres on gcp", () => {
    const planned = planOn("gcp");
    for (const cell of POSTGRES_NEXT) {
      expect(planned.skipped[cell] ?? []).toEqual([]);
    }
  });

  it("deploys deploy/next behind the load balancer on gcp when the run names a zone", () => {
    const zoned = {
      OCEL_E2E_ZONE: "journeys.example.com",
      CLOUDFLARE_API_TOKEN: "token",
      CLOUDFLARE_ACCOUNT_ID: "account",
    };
    expect(planOn("gcp", zoned).cells.map((cell) => cell.name)).toContain("deploy/next-alb");
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
      gcp: ["deploy/next-container", "lifecycle/next-container"],
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

  it("lists every check that reads a page from the Next server's cache red under #1815 on a Next container on gcp, which nothing seeds", () => {
    const readFromNextCache = [
      "the Next server serves a static page from its cache, frozen, with assets immutable for a year",
      "the Next server keeps an ISR page frozen inside its revalidate window and moves it after",
      "revalidating a path makes the Next server move the page it names and nothing else",
      "revalidating a tag makes the Next server move the page that carries it and nothing else",
      "a server action calling updateTag makes the Next server move the page it revalidates",
    ];
    for (const lane of ["gcp", "gcp.floci"] as const) {
      const planned = planOn(lane, {}, EVERY_CELL);
      const listed = planned.expectedFailures["deploy/next-container/web"] ?? {};
      for (const title of readFromNextCache) {
        const key = Object.keys(listed).find((one) => one.endsWith(title));
        expect({ title, issues: key ? listed[key]!.map((gap) => gap.issue) : [] }).toEqual({
          title,
          issues: expect.arrayContaining([1815]),
        });
      }
    }
  });

  it("holds a Next app behind the load balancer on gcp to the edge's cache", () => {
    const planned = planOn("gcp", ZONED, EVERY_CELL);
    const titles = titlesOf(planned, "deploy/next-alb");

    expect(cacheTitlesIn(titles, EDGE_TITLES)).not.toEqual([]);
    expect(cacheTitlesIn(titles, ORIGIN_TITLES)).toEqual([]);
  });

  it("checks that a Next app behind the load balancer on gcp is served its immutable files by the backend bucket, and no other app", () => {
    const planned = planOn("gcp", ZONED, EVERY_CELL);
    const backendBucket = (cell: string) =>
      titlesOf(planned, cell).filter((title) => title.includes("backend bucket"));

    expect(backendBucket("deploy/next-alb")).not.toEqual([]);
    expect(backendBucket("deploy/node-alb")).toEqual([]);
    expect(backendBucket("deploy/next")).toEqual([]);
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

describe("what only a deployment answers", () => {
  const DEPLOYED_ONLY = [
    "GET /api/probes/runtime runs as production in UTC",
    "GET /api/probes/headers sees the real client address behind a forged one",
    ...[...nextCacheChecks, ...nextDataCacheChecks].map((one) => one.title),
  ];

  it("is neither run nor listed red on dev, which serves the app's own dev server on the runner", () => {
    const planned = planOn("dev");
    for (const cell of ["deploy/next", "deploy/node", "sdk/next", "sdk/node"]) {
      const listed = Object.keys(planned.expectedFailures[`${cell}/web`] ?? {});
      expect(listed.filter((title) => DEPLOYED_ONLY.some((one) => title.endsWith(one)))).toEqual(
        [],
      );
    }
  });
});

describe("sdk/next and sdk/sveltekit", () => {
  const EVERY_CELL = { ...NO_FILTER, runSkipped: true };
  const cellsOn = (lane: Lane) => planOn(lane, {}, EVERY_CELL).cells.map((cell) => cell.name);

  it("deploys the Next app on every lane, built from its Dockerfile on a box too, and the SvelteKit app on every lane", () => {
    for (const lane of LANES) {
      expect(cellsOn(lane)).toEqual(expect.arrayContaining(["sdk/next", "sdk/sveltekit"]));
    }
    for (const lane of ["vps", "vps.incus"] as const) {
      expect(cellsOn(lane)).toContain("sdk/next-dockerfile");
    }
  });

  it("sets the build variables, the client values and the plain and secret values on every target", () => {
    const next = fixtures.find((one) => one.name === "sdk/next");
    expect(next?.checks).toEqual(expect.arrayContaining(buildVariablesChecks));
    expect(next?.checks).toEqual(expect.arrayContaining(nextClientEnvChecks));
    expect(next?.checks).toEqual(expect.arrayContaining(prerenderChecks));
    expect(next?.checks).toEqual(expect.arrayContaining(prerenderBucketChecks));
  });

  it("checks that only the image built from the Dockerfile shows no binding", () => {
    const planned = planOn("vps");
    const titlesOf = (cell: string) =>
      planned.cells.find((one) => one.name === cell)?.steps.map((step) => step.title) ?? [];
    expect(titlesOf("sdk/next-dockerfile")).toContain(imageShowsNoBindingCheck.title);
    expect(titlesOf("sdk/next")).not.toContain(imageShowsNoBindingCheck.title);
  });

  it("expects the deployment url red on gcp, where a cell declares no hostname", () => {
    const { expectedFailures } = planOn("gcp");
    for (const cell of ["sdk/next/web", "sdk/sveltekit/web"]) {
      expect(
        expectedFailures[cell]?.[clientEnvDeploymentUrlCheck.title]?.map((gap) => gap.id),
      ).toEqual(["cloud-run-hands-no-deployment-url"]);
    }
  });

  it("expects nothing of the SvelteKit app red where the deploy writes the deployment url", () => {
    for (const lane of ["aws", "dev", "vps", "vps.incus"] as const) {
      expect(planOn(lane).expectedFailures["sdk/sveltekit/web"] ?? {}).toEqual({});
    }
  });

  it("skips both cells on floci's aws, whose stubbed CloudFront routes no domain", () => {
    const planned = planOn("aws.floci");
    expect(planned.skipped["sdk/next"]?.map((gap) => gap.issue)).toEqual([852]);
    expect(planned.skipped["sdk/sveltekit"]?.map((gap) => gap.issue)).toEqual([852]);
  });
});

describe("lifecycle/next", () => {
  it("migrates in lifecycle.preBuild and prerenders what it migrated, on every target that keeps releases", () => {
    const next = fixtures.find((one) => one.name === "lifecycle/next");
    expect(next?.checks).toEqual(
      expect.arrayContaining([...preBuildChecks, ...preBuildPrerenderChecks]),
    );
    for (const lane of ["aws", "gcp", "vps", "vps.incus"] as const) {
      expect(planOn(lane).cells.map((cell) => cell.name)).toContain("lifecycle/next");
    }
  });
});

describe("sdk/go, sdk/python and sdk/rust", () => {
  const CELLS = ["sdk/go", "sdk/python", "sdk/rust"];

  it("drives every primitive on dev and on aws in every language, expecting nothing red", () => {
    for (const lane of ["dev", "aws"] as const) {
      const planned = planOn(lane);
      expect(planned.cells.map((cell) => cell.name)).toEqual(expect.arrayContaining(CELLS));
      for (const cell of CELLS) {
        expect(planned.skipped[cell]).toBeUndefined();
        expect(planned.expectedFailures[`${cell}/web`] ?? {}).toEqual({});
      }
    }
  });

  it("skips every cell on floci's aws, which runs no AppSync Event API", () => {
    const skipped = planOn("aws.floci").skipped;
    for (const cell of [...CELLS, "sdk/go-api-gateway"]) {
      const gap = skipped[cell]?.find((one) => one.id === "floci-runs-no-event-api");
      expect(gap?.reason).toMatch(/Event API/);
    }
  });

  it("skips python as a container on aws behind cloudflare under #1551, whose worker aws refuses on container compute, and where no zone or Cloudflare API is", () => {
    const cell = "sdk/python-cloudflare-container";
    expect(skippedBy(planOn("aws", ZONED_RUN), cell)).toEqual(["aws-container-hosts-no-worker"]);
    expect(skippedBy(planOn("aws"), cell)).toEqual(
      ["aws-container-hosts-no-worker", "no-cloudflare-zone"].sort(),
    );
    expect(skippedBy(planOn("aws.floci"), cell)).toEqual(
      ["aws-container-hosts-no-worker", "floci-runs-no-event-api", "no-cloudflare-api"].sort(),
    );
    expect(gaps.find((gap) => gap.id === "aws-container-hosts-no-worker")?.issue).toBe(1551);
  });

  it("skips every language on a box under the tickets for their images", () => {
    for (const lane of ["vps", "vps.incus"] as const) {
      expect(
        Object.fromEntries(
          Object.entries(planOn(lane).skipped)
            .filter(([cell]) => CELLS.includes(cell))
            .map(([cell, gaps]) => [cell, gaps.map((gap) => gap.issue)]),
        ),
      ).toEqual({ "sdk/go": [1550], "sdk/python": [1598], "sdk/rust": [1598] });
    }
  });

  it("runs Go on gcp, expecting nothing red, and skips Python and Rust under the tickets for their builds", () => {
    const planned = planOn("gcp");
    expect(planned.skipped["sdk/go"]).toBeUndefined();
    expect(planned.expectedFailures["sdk/go/web"] ?? {}).toEqual({});
    expect(planned.skipped["sdk/python"]?.map((gap) => gap.issue)).toEqual([1260]);
    expect(planned.skipped["sdk/rust"]?.map((gap) => gap.issue)).toEqual([1609]);
  });

  it("skips every language on floci-gcp, which serves no Cloud SQL", () => {
    const skipped = planOn("gcp.floci").skipped;
    for (const cell of CELLS) {
      expect(skipped[cell]?.map((gap) => gap.id)).toContain("floci-serves-no-cloud-sql");
    }
  });
});

describe("a Python worker and a Next.js worker", () => {
  const WIRE_TITLES = tasksWireChecks({
    task: "verbatim",
    topic: "notices",
    consumer: "notice-log",
  }).map((one) => one.title);
  const WORKER_FIXTURES = ["sdk/python", "sdk/next"];

  it("run the wire checks in every cell of sdk/python and sdk/next a lane plans", () => {
    const driven = LANES.flatMap((lane) =>
      planOn(lane).cells.filter((cell) => WORKER_FIXTURES.includes(cell.fixture)),
    );
    expect(driven.length).toBeGreaterThan(0);
    for (const cell of driven) {
      expect(cell.steps.map((step) => step.title)).toEqual(expect.arrayContaining(WIRE_TITLES));
    }
  });

  it("expect none of the wire checks red on dev or aws", () => {
    for (const lane of ["dev", "aws"] as const) {
      const { cells, expectedFailures } = planOn(lane);
      for (const fixture of WORKER_FIXTURES) {
        expect(cells.map((cell) => cell.name)).toContain(fixture);
        const listed = Object.keys(expectedFailures[`${fixture}/web`] ?? {});
        expect(listed.filter((title) => WIRE_TITLES.some((one) => title.endsWith(one)))).toEqual(
          [],
        );
      }
    }
  });
});

describe("the fixtures a lane deploys", () => {
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
        const base = path.join(dir, JSON_BASE);
        if (!existsSync(base)) {
          continue;
        }
        const config = JSON.parse(stripJsonComments(readFileSync(base, "utf8"))) as {
          apps?: {
            name: string;
            path: string;
            compute?: string | { serverless?: { framework?: string } };
          }[];
        };
        const changed = fixtures.find((one) => one.name === cell.fixture)?.configOn?.[target]?.apps;
        for (const declared of config.apps ?? []) {
          const app = { ...declared, ...changed?.[declared.name] };
          const appDir = path.join(dir, app.path ?? ".");
          const framework =
            typeof app.compute === "object" ? app.compute.serverless?.framework : undefined;
          if (!framework && !MARKERS.some((marker) => existsSync(path.join(appDir, marker)))) {
            undetectable.add(`${lane}: ${cell.fixture} on ${target} app ${app.name}`);
          }
        }
      }
    }
    expect([...undetectable].sort()).toEqual([]);
  });
});

describe("deploy/next-ppr", () => {
  const cellNames = (lane: Lane) =>
    planOn(lane)
      .cells.map((cell) => cell.name)
      .filter((name) => name === "deploy/next-ppr" || name.startsWith("deploy/next-ppr-"));

  it("deploys the cacheComponents app behind CloudFront and behind Cloudflare on aws, checking its shells and its store reads", () => {
    expect(cellNames("aws").sort()).toEqual(["deploy/next-ppr", "deploy/next-ppr-cloudflare"]);
    const checks = fixtures.find((one) => one.name === "deploy/next-ppr")?.checks ?? [];
    expect(checks).toEqual(expect.arrayContaining([...nextPprChecks, ...nextStoreReadChecks]));
  });

  it("gives no two variants of a fixture the same name", () => {
    for (const fixture of fixtures) {
      for (const [target, variants] of Object.entries(fixture.on)) {
        const names = (variants ?? []).map((variant) => variant.name);
        expect(new Set(names).size, `${fixture.name} on ${target}`).toBe(names.length);
      }
    }
  });

  it("is skipped on floci's aws, which serves neither CloudFront nor the Cloudflare API", () => {
    const skipped = planOn("aws.floci").skipped;
    expect(skipped["deploy/next-ppr"]?.map((gap) => gap.issue)).toEqual([852]);
    expect(skipped["deploy/next-ppr-cloudflare"]?.map((gap) => gap.issue)).toEqual([904]);
  });
});
