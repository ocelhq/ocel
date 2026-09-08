import { execFile } from "node:child_process";
import { access, rm } from "node:fs/promises";
import path from "node:path";
import { promisify } from "node:util";
import { GCP_BASE, JOURNEY_CONFIG, writeJourneyConfig } from "../../config";
import { INITIAL_GREETING, SECRET_TOKEN, UNCAPPED_BODY_BYTES } from "../../contract";
import type { ExpectationEnvironment } from "../../expectations/types";
import { currentRunIdentity, projectSlug, slugPart } from "../../identity";
import { configTree, ocel, runOcel, treeRoot, workTree } from "../../ocel";
import { fixtureDir, treeDir } from "../../paths";
import type { PrepareFailures } from "../../prepare";
import { migrates, setsEnv } from "../../rows";
import { type Cell, cellsOf, type Leg, specForTarget, variantNameOf } from "../../spec";
import { copyTree } from "../../tree";
import { migrateCommand } from "../../workspace";
import type { CellContext, Deployment, Target } from "../types";
import { fittedSlug, namespaceOf, roomForSlug, serviceLead } from "./names";
import {
  deleteService,
  listServices,
  reachable,
  servedBy,
  standing,
  strayServices,
  switchOn,
  type Where,
} from "./store";

const ENDPOINT_ENV = "OCEL_FLOCI_GCP_ENDPOINT";
const PROJECT_ENV = "OCEL_GCP_PROJECT";
const REGION_ENV = "OCEL_GCP_REGION";

const EMULATED_PROJECT = "floci-local";
const DEFAULT_REGION = "europe-west1";

const LEG_TIMEOUT_MS = 900_000;

const BRING_AN_EMULATOR_UP = [
  "scripts/floci.sh --cloud gcp create <name>",
  'eval "$(scripts/floci.sh --cloud gcp status <name>)"',
].join("\n  ");

const ran = promisify(execFile);

let minted: Promise<string | undefined> | undefined;

function endpoint(): string | undefined {
  return process.env[ENDPOINT_ENV]?.trim() || undefined;
}

function project(): string {
  const named = process.env[PROJECT_ENV]?.trim();
  if (named) {
    return named;
  }
  if (!endpoint()) {
    throw new Error(`${PROJECT_ENV} names the project this target deploys into`);
  }
  return EMULATED_PROJECT;
}

function region(): string {
  return process.env[REGION_ENV]?.trim() || DEFAULT_REGION;
}

async function guard(): Promise<ExpectationEnvironment> {
  if (endpoint()) {
    return "gcp.floci";
  }
  if (process.env[PROJECT_ENV]?.trim()) {
    return "gcp";
  }
  throw new Error(
    `neither ${ENDPOINT_ENV} nor ${PROJECT_ENV} is set, and the journey harness never brings an ` +
      `emulator up. Run:\n  ${BRING_AN_EMULATOR_UP}\n\nor name a real project in ${PROJECT_ENV}.`,
  );
}

async function token(): Promise<string | undefined> {
  minted ??= (async () => {
    if (endpoint()) {
      return undefined;
    }
    const { stdout } = await ran("gcloud", ["auth", "print-access-token"]);
    return stdout.trim();
  })();
  return minted;
}

async function where(): Promise<Where> {
  return { endpoint: endpoint(), project: project(), region: region(), token: await token() };
}

async function services() {
  return listServices(await where());
}

function childEnv(dir: string): NodeJS.ProcessEnv {
  return {
    ...process.env,
    OCEL_CONFIG: path.join(dir, JOURNEY_CONFIG),
    [PROJECT_ENV]: project(),
    [REGION_ENV]: region(),
  };
}

function leadsFor(slug: string, apps: string[]): string[] {
  const namespace = namespaceOf(process.env);
  const fitted = fittedSlug(slug, roomForSlug(namespace, apps));
  return apps.map((app) => serviceLead(namespace, fitted, app));
}

function gcpCells(): Cell[] {
  return specForTarget("gcp").flatMap((fixture) => cellsOf(fixture, "gcp"));
}

export function cellOfSlug(cells: Cell[], slug: string): Cell {
  const named = [...cells]
    .sort((a, b) => b.name.length - a.name.length)
    .find((cell) => slug.endsWith(`-${slugPart(cell.name)}`));
  if (!named) {
    throw new Error(`${slug} names no cell this target runs, so nothing says which apps it stands`);
  }
  return named;
}

async function deployment(cell: CellContext, leg: Leg): Promise<Deployment> {
  const standing = await services();
  const leads = leadsFor(cell.slug, cell.fixture.apps);
  const urls = new Map(
    cell.fixture.apps.map((app, at) => {
      const lead = leads[at] ?? "";
      return [app, reachable(servedBy(standing, lead), endpoint())];
    }),
  );
  await cell.evidence.write(
    leg,
    "deployment.json",
    `${JSON.stringify(
      {
        slug: cell.slug,
        variant: variantNameOf(cell),
        project: project(),
        region: region(),
        apps: Object.fromEntries(urls),
      },
      null,
      2,
    )}\n`,
  );
  return {
    baseUrl: (app) => {
      const url = urls.get(app);
      if (!url) {
        throw new Error(`${cell.name} has no app named ${app} on gcp`);
      }
      return url;
    },
    fetch: (...args) => fetch(...args),
  };
}

async function cellTree(cell: CellContext): Promise<string> {
  const dir = configTree(cell, "gcp");
  try {
    await access(dir);
    return dir;
  } catch {
    return workTree(cell, "gcp");
  }
}

async function prepare(): Promise<PrepareFailures> {
  const [first] = specForTarget("gcp");
  if (!first) {
    throw new Error("no fixture in the spec table runs on gcp, so there is nothing to bootstrap");
  }
  const runId = currentRunIdentity();
  const dir = await copyTree(fixtureDir(first.dir), treeDir(runId, "gcp", "bootstrap"));
  try {
    const emulator = endpoint();
    if (emulator) {
      await switchOn(emulator, project());
    }
    await writeJourneyConfig(dir, { base: GCP_BASE, slug: projectSlug(first.name, runId) });
    await ocel(dir, ["bootstrap", "production", "--yes"], childEnv(dir));
  } catch (error) {
    return { lane: error instanceof Error ? error.message : String(error) };
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
  return {};
}

async function up(cell: CellContext): Promise<Deployment> {
  const dir = await cellTree(cell);
  const env = childEnv(dir);

  if (setsEnv(cell.fixture.rows)) {
    await runOcel(
      cell,
      dir,
      "up",
      "env-greeting",
      ["env", "set", "GREETING", INITIAL_GREETING],
      env,
    );
    await runOcel(cell, dir, "up", "env-secret", ["env", "set", "SECRET_TOKEN", SECRET_TOKEN], env);
  }
  await runOcel(cell, dir, "up", "deploy", ["deploy", "--yes"], env);
  if (migrates(cell.fixture.rows)) {
    await runOcel(cell, dir, "up", "migrate", ["run", "--", ...migrateCommand()], env);
  }
  return deployment(cell, "up");
}

async function redeploy(cell: CellContext, greeting: string): Promise<Deployment> {
  const dir = await cellTree(cell);
  const env = childEnv(dir);
  if (setsEnv(cell.fixture.rows)) {
    await runOcel(cell, dir, "redeploy", "env-greeting", ["env", "set", "GREETING", greeting], env);
  }
  await runOcel(cell, dir, "redeploy", "deploy", ["deploy", "--yes"], env);
  return deployment(cell, "redeploy");
}

async function rollback(cell: CellContext): Promise<Deployment> {
  const dir = await cellTree(cell);
  await runOcel(cell, dir, "rollback", "rollback", ["rollback"], childEnv(dir));
  return deployment(cell, "rollback");
}

async function destroy(cell: CellContext): Promise<void> {
  const dir = await cellTree(cell);
  try {
    await runOcel(
      cell,
      dir,
      "destroy",
      "destroy",
      ["destroy", "production", "--yes"],
      childEnv(dir),
    );
  } finally {
    await rm(treeRoot(cell, "gcp"), { recursive: true, force: true });
  }
}

async function stands(slug: string): Promise<boolean> {
  const cell = cellOfSlug(gcpCells(), slug);
  return standing(await services(), leadsFor(slug, cell.fixture.apps));
}

async function list(): Promise<string[]> {
  const runId = currentRunIdentity();
  const found = await services();
  return gcpCells()
    .map((cell) => ({ cell, slug: projectSlug(cell.name, runId) }))
    .filter(({ cell, slug }) => standing(found, leadsFor(slug, cell.fixture.apps)))
    .map(({ slug }) => slug);
}

async function sweepOwn(runId: string): Promise<void> {
  const complaints: string[] = [];
  for (const slug of await list()) {
    const cell = cellOfSlug(gcpCells(), slug);
    const dir = await copyTree(
      fixtureDir(cell.fixture.dir),
      treeDir(runId, "gcp", `sweep-${slug}`),
    );
    try {
      await writeJourneyConfig(dir, { base: GCP_BASE, slug, ...cell.variant?.config });
      await ocel(dir, ["destroy", "production", "--yes"], childEnv(dir));
      process.stdout.write(`swept ${slug}\n`);
    } catch (error) {
      complaints.push(`${slug}: ${String(error)}`);
    } finally {
      await rm(dir, { recursive: true, force: true });
    }
  }
  if (complaints.length > 0) {
    throw new Error(`the gcp sweep left work behind:\n${complaints.join("\n")}`);
  }
}

async function sweep(runId: string): Promise<void> {
  const complaints: string[] = [];
  try {
    await sweepOwn(runId);
  } catch (error) {
    complaints.push(error instanceof Error ? error.message : String(error));
  }
  const mine = gcpCells().flatMap((cell) =>
    leadsFor(projectSlug(cell.name, runId), cell.fixture.apps),
  );
  const found = await services();
  const at = await where();
  for (const name of strayServices(
    found.map((service) => service.name),
    namespaceOf(process.env),
    mine,
  )) {
    try {
      await deleteService(at, name);
      process.stdout.write(`swept ${name}\n`);
    } catch (error) {
      complaints.push(`${name}: ${String(error)}`);
    }
  }
  if (complaints.length > 0) {
    throw new Error(`the gcp sweep left work behind:\n${complaints.join("\n")}`);
  }
}

export const gcpTarget: Target = {
  name: "gcp",
  concurrency: 2,
  largeBodyBytes: UNCAPPED_BODY_BYTES,
  legTimeoutMs: LEG_TIMEOUT_MS,
  legs: ["up", "contract", "redeploy", "rollback", "destroy"],
  guard,
  prepare,
  setup: async () => {
    await guard();
  },
  up,
  redeploy,
  rollback,
  destroy,
  list,
  stands,
  sweep,
  sweepOwn,
};
