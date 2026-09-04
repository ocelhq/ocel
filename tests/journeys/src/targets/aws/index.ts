import { access, rm } from "node:fs/promises";
import path from "node:path";
import { setTimeout as pause } from "node:timers/promises";
import { INITIAL_GREETING, SECRET_TOKEN } from "../../contract";
import type { ExpectationEnvironment } from "../../expectations/types";
import { appHostname, currentRunIdentity, projectSlug } from "../../identity";
import { configTree, copyTree, ocel, runOcel, treeRoot, workTree } from "../../ocel";
import { exampleDir, treeDir } from "../../paths";
import { type Leg, specForTarget } from "../../spec";
import { migrateCommand, setAppNames } from "../../workspace";
import type { CellContext, Deployment, Target } from "../types";
import { authoritativeFetch, emulatorFetch } from "./dispatch";
import { pulumiSweep } from "./ladder-pulumi";
import { sstSweep } from "./ladder-sst";
import { place } from "./place";
import { awaitServing } from "./serving";
import { sweepable } from "./slugs";
import { awsStore, cliAt, said, type Store } from "./store";
import { expectationEnvironmentFor } from "./world";

const CONFIG = "ocel.aws.config.ts";

const LEG_TIMEOUT_MS = process.env.AWS_ENDPOINT_URL ? 600_000 : 1_800_000;

const DEFAULT_VPC_TRIES = 30;
const SERVING_TIMEOUT_MS = 900_000;
const SERVING_INTERVAL_MS = 5_000;

let dispatching: Promise<typeof fetch> | undefined;

const EDGE_FEATURES: Record<string, string> = {
  "api-gateway": "apigateway-edge",
  cloudfront: "cloudfront-edge",
  cloudflare: "cloudflare-edge",
};

const NEXT_FEATURES = ["isr", "image-optimization"];

async function guard(): Promise<ExpectationEnvironment> {
  return expectationEnvironmentFor((await place()).world);
}

function childEnv(dir: string, runId: string): NodeJS.ProcessEnv {
  return {
    ...process.env,
    OCEL_CONFIG: path.join(dir, CONFIG),
    OCEL_JOURNEY_RUN: runId,
  };
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
    cell.example.apps.map((app) => {
      const host = appHostname(app, cell.slug, zone());
      if (!host) {
        throw new Error(`${cell.slug} declares no hostname for ${app}`);
      }
      return [app, host];
    }),
  );
}

async function dispatcher(): Promise<typeof fetch> {
  dispatching ??= (async () => {
    const where = await place();
    return where.endpoint ? emulatorFetch(where.endpoint) : authoritativeFetch(zone());
  })();
  return dispatching;
}

function deployment(cell: CellContext, dispatch: typeof fetch): Deployment {
  const hosts = hostnames(cell);
  return {
    baseUrl: (app) => {
      const host = hosts.get(app);
      if (!host) {
        throw new Error(`${cell.example.name} has no app named ${app} on aws`);
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
  const urls = new Map(cell.example.apps.map((app) => [app, deployed.baseUrl(app)]));
  const served = await awaitServing(deployed.fetch, urls, {
    timeoutMs: SERVING_TIMEOUT_MS,
    intervalMs: SERVING_INTERVAL_MS,
    now: () => Date.now(),
    sleep: (ms) => pause(ms),
  });
  await cell.evidence.write(leg, "serving.json", `${JSON.stringify(served, null, 2)}\n`);
}

function bootstrapFeatures(): string[] {
  const edge = EDGE_FEATURES[process.env.OCEL_AWS_EDGE ?? ""];
  return edge ? [...NEXT_FEATURES, edge] : NEXT_FEATURES;
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

async function prepare(): Promise<void> {
  const where = await place();
  if (where.world === "floci" && where.endpoint) {
    await awaitDefaultVpc(where.endpoint);
  }
  const [first] = specForTarget("aws");
  if (!first) {
    throw new Error("no example in the spec table runs on aws, so there is nothing to bootstrap");
  }
  const runId = currentRunIdentity();
  const dir = await copyTree(exampleDir(first.dir), treeDir(runId, "aws", "bootstrap"));
  const args = ["bootstrap", "production", "--yes", "--features", bootstrapFeatures().join(",")];
  try {
    await ocel(dir, args, childEnv(dir, runId));
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
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
  const env = childEnv(dir, cell.runId);

  await runOcel(cell, dir, "up", "env-greeting", ["env", "set", "GREETING", INITIAL_GREETING], env);
  await runOcel(cell, dir, "up", "env-secret", ["env", "set", "SECRET_TOKEN", SECRET_TOKEN], env);
  await setAppNames(cell.example, (name, args) => runOcel(cell, dir, "up", name, args, env));
  await runOcel(cell, dir, "up", "deploy", ["deploy", "--yes"], env);
  await runOcel(cell, dir, "up", "domain-add", ["domain", "add"], env);

  const deployed = deployment(cell, await dispatcher());
  await awaitEdge(cell, "up", deployed);

  if (cell.example.suites.includes("product")) {
    await runOcel(cell, dir, "up", "migrate", ["run", "--", ...migrateCommand(cell.example)], env);
  }

  await cell.evidence.write(
    "up",
    "deployment.json",
    `${JSON.stringify(
      {
        slug: cell.slug,
        edge: process.env.OCEL_AWS_EDGE ?? "default",
        apps: Object.fromEntries(
          cell.example.apps.map((app) => [app, deployed.baseUrl(app)]),
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
  const env = childEnv(dir, cell.runId);
  await runOcel(cell, dir, "redeploy", "env-greeting", ["env", "set", "GREETING", greeting], env);
  await runOcel(cell, dir, "redeploy", "deploy", ["deploy", "--yes"], env);
  const deployed = deployment(cell, await dispatcher());
  await awaitEdge(cell, "redeploy", deployed);
  return deployed;
}

async function rollback(cell: CellContext): Promise<Deployment> {
  const dir = await cellTree(cell);
  await runOcel(cell, dir, "rollback", "rollback", ["rollback"], childEnv(dir, cell.runId));
  const deployed = deployment(cell, await dispatcher());
  await awaitEdge(cell, "rollback", deployed);
  return deployed;
}

async function destroy(cell: CellContext): Promise<void> {
  const dir = await cellTree(cell);
  const env = childEnv(dir, cell.runId);
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
  const examples = specForTarget("aws");
  const mine = examples.map((example) => projectSlug(example.name, runId));
  const { reclaim, unreadable } = sweepable(
    await list(),
    mine,
    examples.map((example) => example.name),
  );

  const complaints: string[] = unreadable.map(
    (slug) => `${slug} carries the harness prefix and names no example in the spec table`,
  );
  for (const stranded of reclaim) {
    const example = examples.find((row) => row.name === stranded.example);
    if (!example) {
      continue;
    }
    const dir = await copyTree(
      exampleDir(example.dir),
      treeDir(runId, "aws", `sweep-${stranded.slug}`),
    );
    try {
      await ocel(dir, ["destroy", "production", "--yes"], childEnv(dir, stranded.run));
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
    if (!examples.some((example) => example.name === name)) {
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
