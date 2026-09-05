import { access, rm } from "node:fs/promises";
import path from "node:path";
import { setTimeout as pause } from "node:timers/promises";
import { AWS_BASE, JOURNEY_CONFIG, writeJourneyConfig } from "../../config";
import { type Fetch, INITIAL_GREETING, SECRET_TOKEN } from "../../contract";
import type { ExpectationEnvironment } from "../../expectations/types";
import { appHostname, currentRunIdentity, projectSlug, slugPart } from "../../identity";
import { cellEnv, configTree, ocel, runOcel, treeRoot, workTree } from "../../ocel";
import { fixtureDir, treeDir } from "../../paths";
import { migrates, setsEnv } from "../../rows";
import type { PrepareFailures } from "../../prepare";
import { cellsOf, type Leg, specForTarget, variantNameOf } from "../../spec";
import { copyTree } from "../../tree";
import { migrateCommand, setAppNames, setSiteHostnames } from "../../workspace";
import type { CellContext, Deployment, Target } from "../types";
import { authoritativeFetch, emulatorFetch } from "./dispatch";
import { pulumiSweep } from "./ladder-pulumi";
import { sstSweep } from "./ladder-sst";
import { place } from "./place";
import { awaitServing } from "./serving";
import { sweepable } from "./slugs";
import { awsStore, cliAt, said, type Store } from "./store";
import { expectationEnvironmentFor, type World } from "./world";

const LEG_TIMEOUT_MS = process.env.AWS_ENDPOINT_URL ? 600_000 : 1_800_000;

const DEFAULT_VPC_TRIES = 30;
const SERVING_TIMEOUT_MS = 900_000;
const SERVING_INTERVAL_MS = 5_000;

let dispatching: Promise<Fetch> | undefined;

const EVERY_FEATURE = "all";
const FLOCI_FEATURES = ["isr", "image-optimization", "cloudfront-edge", "apigateway-edge"];

async function guard(): Promise<ExpectationEnvironment> {
  return expectationEnvironmentFor((await place()).world);
}

function providerEnv(dir: string, held: NodeJS.ProcessEnv): NodeJS.ProcessEnv {
  return {
    ...process.env,
    OCEL_CONFIG: path.join(dir, JOURNEY_CONFIG),
    ...held,
  };
}

function childEnv(cell: CellContext, dir: string): NodeJS.ProcessEnv {
  return providerEnv(dir, cellEnv(cell));
}

async function store(): Promise<Store> {
  return awsStore((await place()).endpoint);
}

function zone(): string {
  const named = process.env.OCEL_JOURNEY_ZONE;
  if (!named) {
    throw new Error("the aws target reached a cell before it knew which zone to serve on");
  }
  return named;
}

function hostnames(cell: CellContext): Map<string, string> {
  return new Map(
    cell.fixture.apps.map((app) => {
      const host = appHostname(app, cell.slug, zone());
      if (!host) {
        throw new Error(`${cell.slug} declares no hostname for ${app}`);
      }
      return [app, host];
    }),
  );
}

async function dispatcher(): Promise<Fetch> {
  dispatching ??= (async () => {
    const where = await place();
    return where.endpoint ? emulatorFetch(where.endpoint) : authoritativeFetch(zone());
  })();
  return dispatching;
}

function deployment(cell: CellContext, dispatch: Fetch): Deployment {
  const hosts = hostnames(cell);
  return {
    baseUrl: (app) => {
      const host = hosts.get(app);
      if (!host) {
        throw new Error(`${cell.name} has no app named ${app} on aws`);
      }
      return `https://${host}`;
    },
    fetch: dispatch,
  };
}

async function awaitEdge(cell: CellContext, leg: Leg, deployed: Deployment): Promise<void> {
  if ((await place()).world !== "real") {
    return;
  }
  const urls = new Map(cell.fixture.apps.map((app) => [app, deployed.baseUrl(app)]));
  const served = await awaitServing(deployed.fetch, urls, {
    timeoutMs: SERVING_TIMEOUT_MS,
    intervalMs: SERVING_INTERVAL_MS,
    now: () => Date.now(),
    sleep: (ms) => pause(ms),
  });
  await cell.evidence.write(leg, "serving.json", `${JSON.stringify(served, null, 2)}\n`);
}

export function bootstrapFeatures(world: World): string {
  return world === "floci" ? FLOCI_FEATURES.join(",") : EVERY_FEATURE;
}

async function awaitDefaultVpc(endpoint: string): Promise<void> {
  const cli = cliAt(endpoint);
  let last = "";
  for (let attempt = 0; attempt < DEFAULT_VPC_TRIES; attempt++) {
    try {
      const raw = await cli([
        "ec2",
        "describe-vpcs",
        "--filters",
        "Name=isDefault,Values=true",
        "--output",
        "json",
      ]);
      if ((JSON.parse(raw) as { Vpcs?: unknown[] }).Vpcs?.length) {
        return;
      }
      last = "the emulator lists no default VPC";
    } catch (error) {
      last = said(error);
    }
    await pause(1000);
  }
  throw new Error(`the emulator never showed a default VPC, and every deploy looks one up first: ${last}`);
}

async function prepare(): Promise<PrepareFailures> {
  const where = await place();
  if (where.world === "floci" && where.endpoint) {
    await awaitDefaultVpc(where.endpoint);
  }
  const [first] = specForTarget("aws");
  if (!first) {
    throw new Error("no example in the spec table runs on aws, so there is nothing to bootstrap");
  }
  const runId = currentRunIdentity();
  const slug = projectSlug(first.name, runId);
  const dir = await copyTree(fixtureDir(first.dir), treeDir(runId, "aws", "bootstrap"));
  try {
    await writeJourneyConfig(dir, { base: AWS_BASE, slug });
    await ocel(
      dir,
      ["bootstrap", "production", "--yes", "--features", bootstrapFeatures(where.world)],
      providerEnv(dir, {}),
    );
  } catch (error) {
    return { lane: error instanceof Error ? error.message : String(error) };
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
  return {};
}

async function setup(): Promise<void> {
  await place();
}

async function cellTree(cell: CellContext): Promise<string> {
  const dir = configTree(cell, "aws");
  try {
    await access(dir);
    return dir;
  } catch {
    return workTree(cell, "aws");
  }
}

async function up(cell: CellContext): Promise<Deployment> {
  const dir = await cellTree(cell);
  const env = childEnv(cell, dir);

  if (setsEnv(cell.fixture.rows)) {
    await runOcel(cell, dir, "up", "env-greeting", ["env", "set", "GREETING", INITIAL_GREETING], env);
    await runOcel(cell, dir, "up", "env-secret", ["env", "set", "SECRET_TOKEN", SECRET_TOKEN], env);
  }
  await setAppNames(cell.fixture, (name, args) => runOcel(cell, dir, "up", name, args, env));
  await setSiteHostnames(cell.fixture, hostnames(cell), (name, args) =>
    runOcel(cell, dir, "up", name, args, env),
  );
  await runOcel(cell, dir, "up", "deploy", ["deploy", "--yes"], env);
  await runOcel(cell, dir, "up", "domain-add", ["domain", "add"], env);
  await runOcel(cell, dir, "up", "deploy-bound", ["deploy", "--yes"], env);

  const deployed = deployment(cell, await dispatcher());
  await awaitEdge(cell, "up", deployed);

  if (migrates(cell.fixture.rows)) {
    await runOcel(cell, dir, "up", "migrate", ["run", "--", ...migrateCommand()], env);
  }

  await cell.evidence.write(
    "up",
    "deployment.json",
    `${JSON.stringify(
      {
        slug: cell.slug,
        variant: variantNameOf(cell),
        apps: Object.fromEntries(
          cell.fixture.apps.map((app) => [app, deployed.baseUrl(app)]),
        ),
      },
      null,
      2,
    )}\n`,
  );
  return deployed;
}

async function redeploy(cell: CellContext, greeting: string): Promise<Deployment> {
  const dir = await cellTree(cell);
  const env = childEnv(cell, dir);
  if (setsEnv(cell.fixture.rows)) {
    await runOcel(cell, dir, "redeploy", "env-greeting", ["env", "set", "GREETING", greeting], env);
  }
  await runOcel(cell, dir, "redeploy", "deploy", ["deploy", "--yes"], env);
  const deployed = deployment(cell, await dispatcher());
  await awaitEdge(cell, "redeploy", deployed);
  return deployed;
}

async function rollback(cell: CellContext): Promise<Deployment> {
  const dir = await cellTree(cell);
  await runOcel(cell, dir, "rollback", "rollback", ["rollback"], childEnv(cell, dir));
  const deployed = deployment(cell, await dispatcher());
  await awaitEdge(cell, "rollback", deployed);
  return deployed;
}

async function destroy(cell: CellContext): Promise<void> {
  const dir = await cellTree(cell);
  const env = childEnv(cell, dir);
  const hosts = hostnames(cell);
  for (const [app, host] of hosts) {
    await runOcel(cell, dir, "destroy", `domain-rm-${app}`, ["domain", "rm", host], env);
  }
  await runOcel(cell, dir, "destroy", "destroy", ["destroy", "production", "--yes"], env);
  await rm(treeRoot(cell, "aws"), { recursive: true, force: true });
}

async function list(): Promise<string[]> {
  return (await store()).deployedSlugs();
}

async function stands(slug: string): Promise<boolean> {
  return (await store()).stands(slug);
}

async function sweep(runId: string): Promise<void> {
  const where = await place();
  const fixtures = specForTarget("aws");
  const cells = fixtures.flatMap((fixture) => cellsOf(fixture, "aws"));
  const byPart = new Map(cells.map((cell) => [slugPart(cell.name), cell]));
  const mine = cells.map((cell) => projectSlug(cell.name, runId));
  const { reclaim, unreadable } = sweepable(await list(), mine, [...byPart.keys()]);

  const complaints: string[] = unreadable.map(
    (slug) => `${slug} carries the harness prefix and names no cell in the spec table`,
  );
  for (const stranded of reclaim) {
    const cell = byPart.get(stranded.cell);
    if (!cell) {
      continue;
    }
    const dir = await copyTree(
      fixtureDir(cell.fixture.dir),
      treeDir(runId, "aws", `sweep-${stranded.slug}`),
    );
    try {
      await writeJourneyConfig(dir, { base: AWS_BASE, slug: stranded.slug });
      await ocel(dir, ["destroy", "production", "--yes"], providerEnv(dir, {}));
      process.stdout.write(`swept ${stranded.slug}\n`);
    } catch (error) {
      complaints.push(`${stranded.slug}: ${String(error)}`);
    } finally {
      await rm(dir, { recursive: true, force: true });
    }
  }

  const left = new Set(await list());
  for (const stranded of reclaim) {
    if (left.has(stranded.slug)) {
      complaints.push(`${stranded.slug} still stands after the sweep destroyed it`);
    }
  }

  const ladderSweeps: Array<[string, (runId: string) => Promise<void>]> = [
    ["with-sst", sstSweep],
    ["with-pulumi", pulumiSweep],
  ];
  for (const [name, sweepLadder] of ladderSweeps) {
    if (!fixtures.some((fixture) => fixture.name === name)) {
      continue;
    }
    try {
      await sweepLadder(runId);
    } catch (error) {
      complaints.push(`${name} ladder sweep: ${String(error)}`);
    }
  }

  if (complaints.length === 0) {
    return;
  }
  const said = `the aws sweep left work behind:\n${complaints.join("\n")}`;
  if (where.world === "real") {
    throw new Error(said);
  }
  process.stderr.write(`${said}\n`);
}

export const awsTarget: Target = {
  name: "aws",
  concurrency: 3,
  legTimeoutMs: LEG_TIMEOUT_MS,
  legs: ["up", "contract", "redeploy", "rollback", "destroy"],
  guard,
  prepare,
  setup,
  up,
  redeploy,
  rollback,
  destroy,
  list,
  stands,
  sweep,
};
