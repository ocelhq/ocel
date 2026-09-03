import { access, mkdir, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import { INITIAL_GREETING, SECRET_TOKEN } from "../../contract";
import type { ExpectationEnvironment } from "../../expectations/types";
import { appHostname, currentRunIdentity, projectSlug } from "../../identity";
import { copyTree, ocel, runOcel, workTree } from "../../ocel";
import { exampleDir, outputRoot, treeDir } from "../../paths";
import { specForTarget } from "../../spec";
import type { CellContext, Deployment, Target } from "../types";
import { emulatorFetch } from "./dispatch";
import { sweepable } from "./slugs";
import { answersAsFloci, callerAccount, deployedSlugs } from "./store";
import { detectWorld, expectationEnvironmentFor, type Where } from "./world";

const CONFIG = "ocel.aws.config.ts";

const FLOCI_ZONE = "journey.test";

const DEFAULT_REGION = "us-east-1";

const LEG_TIMEOUT_MS = 600_000;

const pinnedDir = path.join(outputRoot, "aws-account");

let placed: Promise<Where> | undefined;
let bootstrapped: Promise<void> | undefined;
let dispatching: Promise<typeof fetch> | undefined;

async function pinAccountFiles(): Promise<void> {
  await mkdir(pinnedDir, { recursive: true });
  const config = path.join(pinnedDir, "config");
  const credentials = path.join(pinnedDir, "credentials");
  await writeFile(config, "", "utf8");
  await writeFile(credentials, "", "utf8");
  process.env.AWS_CONFIG_FILE = config;
  process.env.AWS_SHARED_CREDENTIALS_FILE = credentials;
  delete process.env.AWS_PROFILE;
  delete process.env.AWS_DEFAULT_PROFILE;
  process.env.AWS_REGION ??= DEFAULT_REGION;
  process.env.AWS_DEFAULT_REGION ??= process.env.AWS_REGION;
}

async function place(): Promise<Where> {
  placed ??= (async () => {
    await pinAccountFiles();
    const where = await detectWorld(process.env, {
      answersAsFloci,
      callerAccount: () => callerAccount(process.env.AWS_ENDPOINT_URL),
    });
    if (where.world === "floci") {
      process.env.AWS_ACCESS_KEY_ID ??= "test";
      process.env.AWS_SECRET_ACCESS_KEY ??= "test";
      process.env.OCEL_JOURNEY_ZONE ??= FLOCI_ZONE;
    } else if (!process.env.OCEL_JOURNEY_ZONE) {
      throw new Error(
        "OCEL_JOURNEY_ZONE names the zone this run's production hostnames hang under, and an aws project with no production hostname has nowhere to serve",
      );
    }
    return where;
  })();
  return placed;
}

async function guard(): Promise<ExpectationEnvironment> {
  return expectationEnvironmentFor((await place()).world);
}

function childEnv(cell: CellContext, dir: string): NodeJS.ProcessEnv {
  return {
    ...process.env,
    OCEL_CONFIG: path.join(dir, CONFIG),
    OCEL_JOURNEY_RUN: cell.runId,
  };
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
    return where.endpoint ? emulatorFetch(where.endpoint) : fetch;
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

async function bootstrap(): Promise<void> {
  bootstrapped ??= (async () => {
    const [first] = specForTarget("aws");
    if (!first) {
      throw new Error("no example in the spec table runs on aws, so there is nothing to bootstrap");
    }
    const runId = currentRunIdentity();
    const dir = await copyTree(exampleDir(first.dir), treeDir(runId, "aws", "bootstrap"));
    try {
      await ocel(dir, ["bootstrap", "production", "--yes"], {
        ...process.env,
        OCEL_CONFIG: path.join(dir, CONFIG),
        OCEL_JOURNEY_RUN: runId,
      });
    } finally {
      await rm(dir, { recursive: true, force: true });
    }
  })();
  return bootstrapped;
}

async function setup(): Promise<void> {
  await place();
  await bootstrap();
}

async function cellTree(cell: CellContext): Promise<string> {
  const dir = treeDir(cell.runId, "aws", cell.example.name);
  try {
    await access(dir);
    return dir;
  } catch {
    return workTree(cell, "aws");
  }
}

async function up(cell: CellContext): Promise<Deployment> {
  const dir = await workTree(cell, "aws");
  const env = childEnv(cell, dir);

  await runOcel(cell, dir, "up", "env-greeting", ["env", "set", "GREETING", INITIAL_GREETING], env);
  await runOcel(cell, dir, "up", "env-secret", ["env", "set", "SECRET_TOKEN", SECRET_TOKEN], env);
  await runOcel(cell, dir, "up", "deploy", ["deploy", "--yes"], env);
  await runOcel(cell, dir, "up", "domain-add", ["domain", "add"], env);
  if (cell.example.suites.includes("product")) {
    await runOcel(cell, dir, "up", "migrate", ["run", "--", "pnpm", "migrate"], env);
  }

  const deployed = deployment(cell, await dispatcher());
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
  const env = childEnv(cell, dir);
  await runOcel(cell, dir, "redeploy", "env-greeting", ["env", "set", "GREETING", greeting], env);
  await runOcel(cell, dir, "redeploy", "deploy", ["deploy", "--yes"], env);
  return deployment(cell, await dispatcher());
}

async function rollback(cell: CellContext): Promise<Deployment> {
  const dir = await cellTree(cell);
  await runOcel(cell, dir, "rollback", "rollback", ["rollback"], childEnv(cell, dir));
  return deployment(cell, await dispatcher());
}

async function destroy(cell: CellContext): Promise<void> {
  const dir = await cellTree(cell);
  const env = childEnv(cell, dir);
  const hosts = hostnames(cell);
  for (const [app, host] of hosts) {
    await runOcel(cell, dir, "destroy", `domain-rm-${app}`, ["domain", "rm", host], env);
  }
  await runOcel(cell, dir, "destroy", "destroy", ["destroy", "production", "--yes"], env);
  await rm(dir, { recursive: true, force: true });
}

async function list(): Promise<string[]> {
  return deployedSlugs((await place()).endpoint);
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
    const env = {
      ...process.env,
      OCEL_CONFIG: path.join(dir, CONFIG),
      OCEL_JOURNEY_RUN: stranded.run,
    };
    try {
      await ocel(dir, ["destroy", "production", "--yes"], env);
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
  setup,
  up,
  redeploy,
  rollback,
  destroy,
  list,
  sweep,
};
