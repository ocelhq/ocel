import { type ChildProcess, execFile } from "node:child_process";
import { access, readFile, rm } from "node:fs/promises";
import path from "node:path";
import { promisify } from "node:util";
import { BUILD_VARIABLES, setsBuildVariables, setsEnv, setsSecret } from "../../checks";
import {
  INITIAL_GREETING,
  JOURNEY_NONCE_ENV,
  SECRET_TOKEN,
  setsJourneyNonce,
  UNCAPPED_BODY_BYTES,
} from "../../checks/context";
import { journeyConfigIn, type Overlay, overlayFor, writeJourneyConfig } from "../../config";
import { currentRunIdentity, projectSlug, slugPart } from "../../identity";
import { fixtures as matrix } from "../../matrix/fixtures";
import type { Cell, Lane, Phase } from "../../matrix/types";
import { sanitize } from "../../naming";
import { configTree, ocel, type Ran, recordOutput, runOcel, treeRoot, workTree } from "../../ocel";
import { fixtureDir, laneDir, treeDir } from "../../paths";
import { cellsOn, fixturesOn, type Plan } from "../../plan";
import type { PrepareFailures } from "../../prepare";
import type { CellUnderTest } from "../../run/cellRun";
import { copyTree } from "../../tree";
import { hostnameUrls } from "../hostnames";
import { previewReleasesIn } from "../previewResult";
import type {
  Deployment,
  Exposure,
  PreviewRelease,
  Previews,
  ReleaseCycle,
  Restart,
  Sweeper,
  Target,
} from "../types";
import { appBucketPrefix, deleteAppBucket, listAppBuckets, strayBuckets } from "./buckets";
import { databaseFilter, deleteDatabase, listDatabases, strayDatabases } from "./cloudsql";
import { startDispatch, stopDispatch } from "./dispatch";
import {
  createTimesIn,
  deleteStore,
  listStores,
  NETWORK_FEATURE,
  simulateMaintenance,
  storeFilter,
  strayStores,
} from "./memorystore";
import { gcpSlug, namespaceOf } from "./names";
import {
  ALB_FEATURE,
  accessToken,
  deleteService,
  exposedServices,
  findAppService,
  listServices,
  reachable,
  readServices,
  type Service,
  servicesOf,
  strayServices,
  switchOn,
  TASKS_FEATURE,
  type Where,
} from "./store";

const ENDPOINT_ENV = "OCEL_FLOCI_GCP_ENDPOINT";
const FIRESTORE_ENDPOINT_ENV = "OCEL_FLOCI_FIRESTORE_ENDPOINT";
const TASKS_ENDPOINT_ENV = "OCEL_FLOCI_TASKS_ENDPOINT";
const PROJECT_ENV = "OCEL_GCP_PROJECT";
const REGION_ENV = "OCEL_GCP_REGION";

const EMULATED_PROJECT = "floci-local";
const DEFAULT_REGION = "europe-west1";

const BRING_AN_EMULATOR_UP = [
  "scripts/floci.sh --cloud gcp create <name>",
  "export $(scripts/floci.sh --cloud gcp status <name>)",
].join("\n  ");

export function refuseFlociWithoutFirestore(env: NodeJS.ProcessEnv): Error | undefined {
  if (!env[ENDPOINT_ENV]?.trim() || env[FIRESTORE_ENDPOINT_ENV]?.trim()) {
    return undefined;
  }
  return new Error(
    `the floci lane serves tag records from Google's Firestore emulator, which scripts/floci.sh --cloud gcp starts beside floci, and ${FIRESTORE_ENDPOINT_ENV} names none. Run:\n  ${BRING_AN_EMULATOR_UP}`,
  );
}

const ran = promisify(execFile);

async function gcloudAccessToken(): Promise<string> {
  const { stdout } = await ran("gcloud", ["auth", "print-access-token"]);
  return stdout.trim();
}

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

function childEnv(dir: string): NodeJS.ProcessEnv {
  return {
    ...process.env,
    OCEL_CONFIG: path.join(dir, journeyConfigIn(dir)),
    [PROJECT_ENV]: project(),
    [REGION_ENV]: region(),
  };
}

function gcpCells(): Cell[] {
  return fixturesOn(matrix, "gcp").flatMap((fixture) => cellsOn(fixture, "gcp"));
}

export function cellOfSlug(cells: Cell[], slug: string): Cell {
  const named = [...cells]
    .sort((a, b) => b.name.length - a.name.length)
    .find((cell) => slug.endsWith(`-${slugPart(cell.name)}`));
  if (!named) {
    throw new Error(
      `${slug} names no cell this target runs, so nothing says which apps it deploys`,
    );
  }
  return named;
}

async function cellTree(cell: CellUnderTest): Promise<string> {
  const dir = configTree(cell, "gcp");
  try {
    await access(dir);
    return dir;
  } catch {
    return workTree(cell, "gcp");
  }
}

export function gcpSweepOverlay(cell: Cell, slug: string, env: NodeJS.ProcessEnv): Overlay {
  return overlayFor(
    { name: cell.name, slug, fixture: cell.fixture, variant: cell.variant },
    "gcp",
    env,
  );
}

function hasCloudflareZone(env: NodeJS.ProcessEnv): boolean {
  return ["OCEL_E2E_ZONE", "CLOUDFLARE_API_TOKEN", "CLOUDFLARE_ACCOUNT_ID"].every((name) =>
    env[name]?.trim(),
  );
}

export function laneFeatures(env: NodeJS.ProcessEnv, emulated: boolean): string[] {
  if (emulated) {
    return [TASKS_FEATURE];
  }
  return [NETWORK_FEATURE, TASKS_FEATURE, ...(hasCloudflareZone(env) ? [ALB_FEATURE] : [])];
}

export function previewBootstrapArgs(
  env: NodeJS.ProcessEnv,
  emulated: boolean,
  planned: { cells: { phases: string[] }[] },
): string[] | undefined {
  if (emulated || !planned.cells.some((cell) => cell.phases.includes("preview"))) {
    return undefined;
  }
  const features = [TASKS_FEATURE, ...(hasCloudflareZone(env) ? [ALB_FEATURE] : [])];
  return ["bootstrap", "preview", "--yes", "--features", features.join(",")];
}

export class GcpTarget implements Target, ReleaseCycle, Restart, Exposure, Previews {
  readonly name = "gcp";
  readonly workers = 2;
  readonly maxRequestBodyBytes = UNCAPPED_BODY_BYTES;
  readonly stepTimeoutMs = 900_000;
  readonly previewLanes: Lane[] = ["gcp"];
  readonly previewStepTimeoutMs = 2_700_000;

  private dispatching: ChildProcess | undefined;

  private readonly previewsUp = new Map<string, number>();

  private readonly output = new Map<string, string[]>();

  readonly sweeper: Sweeper = {
    list: () => this.deployedSlugs(),
    exists: async (slug) => {
      const cell = cellOfSlug(gcpCells(), slug);
      return this.deploys(
        await this.services(),
        gcpSlug({ slug, fixture: cell.fixture }, process.env),
      );
    },
    sweepStale: (runId) => this.sweepStale(runId),
    sweepRun: (runId) => this.sweepRun(runId),
  };

  async detectLane(): Promise<Lane> {
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

  async prepareLane(planned: Pick<Plan, "cells">): Promise<PrepareFailures> {
    const refused = refuseFlociWithoutFirestore(process.env);
    if (refused) {
      return { lane: refused.message };
    }
    const [first] = fixturesOn(matrix, "gcp");
    if (!first) {
      throw new Error("no fixture in the matrix runs on gcp, so there is nothing to bootstrap");
    }
    const runId = currentRunIdentity();
    const dir = await copyTree(fixtureDir(first.name), treeDir(runId, "gcp", "bootstrap"));
    try {
      const emulator = endpoint();
      if (emulator) {
        await switchOn(emulator, project());
      }
      await writeJourneyConfig(dir, {
        target: "gcp",
        slug: projectSlug(path.posix.basename(first.name), runId),
      });
      const features = laneFeatures(process.env, emulator !== undefined);
      await ocel(
        dir,
        ["bootstrap", "production", "--yes", "--features", features.join(",")],
        childEnv(dir),
      );
      const preview = previewBootstrapArgs(process.env, emulator !== undefined, planned);
      if (preview) {
        await ocel(dir, preview, childEnv(dir));
      }
      if (emulator) {
        const { child, tasksEndpoint } = await startDispatch(
          emulator,
          project(),
          region(),
          laneDir(runId, "gcp"),
        );
        this.dispatching = child;
        process.env[TASKS_ENDPOINT_ENV] = tasksEndpoint;
      }
    } catch (error) {
      return { lane: error instanceof Error ? error.message : String(error) };
    } finally {
      await rm(dir, { recursive: true, force: true });
    }
    return {};
  }

  async finishLane(): Promise<void> {
    await stopDispatch(this.dispatching);
    this.dispatching = undefined;
    delete process.env[TASKS_ENDPOINT_ENV];
  }

  async prepareProcess(): Promise<void> {
    await this.detectLane();
  }

  async deploy(cell: CellUnderTest): Promise<Deployment> {
    const dir = await cellTree(cell);
    const env = childEnv(dir);

    if (setsEnv(cell.fixture.checks)) {
      await this.run(
        cell,
        dir,
        "deploy",
        "env-greeting",
        ["env", "set", `GREETING=${INITIAL_GREETING}`],
        env,
      );
    }
    if (setsSecret(cell.fixture.checks)) {
      await this.run(
        cell,
        dir,
        "deploy",
        "env-secret",
        ["env", "set", `SECRET_TOKEN=${SECRET_TOKEN}`],
        env,
      );
    }
    if (setsBuildVariables(cell.fixture.checks)) {
      for (const [key, value] of Object.entries(BUILD_VARIABLES)) {
        await this.run(cell, dir, "deploy", `env-${key}`, ["env", "set", `${key}=${value}`], env);
      }
    }
    if (setsJourneyNonce(cell.fixture.checks)) {
      await this.run(
        cell,
        dir,
        "deploy",
        "env-journey-nonce",
        ["env", "set", `${JOURNEY_NONCE_ENV}=${cell.journeyNonce}`],
        env,
      );
    }
    const deployed = await this.run(cell, dir, "deploy", "deploy", ["deploy", "--yes"], env);
    const created = createTimesIn(`${deployed.stdout}\n${deployed.stderr}`);
    if (created.length > 0) {
      await cell.evidence.write(
        "deploy",
        "kv-create.json",
        `${JSON.stringify(created, null, 2)}\n`,
      );
    }
    return this.deployment(cell, "deploy");
  }

  async redeploy(cell: CellUnderTest, greeting: string): Promise<Deployment> {
    const dir = await cellTree(cell);
    const env = childEnv(dir);
    if (setsEnv(cell.fixture.checks)) {
      await this.run(
        cell,
        dir,
        "redeploy",
        "env-greeting",
        ["env", "set", `GREETING=${greeting}`],
        env,
      );
    }
    await this.run(cell, dir, "redeploy", "deploy", ["deploy", "--yes"], env);
    return this.deployment(cell, "redeploy");
  }

  async rollback(cell: CellUnderTest): Promise<Deployment> {
    const dir = await cellTree(cell);
    await this.run(cell, dir, "rollback", "rollback", ["rollback", "--yes"], childEnv(dir));
    return this.deployment(cell, "rollback");
  }

  async restart(cell: CellUnderTest): Promise<Deployment> {
    const where = await this.where();
    const slug = gcpSlug(cell, process.env);
    const stores = await listStores(where, storeFilter(namespaceOf(process.env), slug));
    await cell.evidence.write("restart", "restarted.json", `${JSON.stringify(stores, null, 2)}\n`);
    if (stores.length === 0) {
      throw new Error(
        `no Memorystore instance is labelled ocel-project=${slug}, so nothing ${cell.name} declared was restarted`,
      );
    }
    for (const store of stores) {
      await simulateMaintenance(where, store.name);
    }
    return this.deployment(cell, "restart");
  }

  async readExposed(cell: CellUnderTest): Promise<string> {
    const services = exposedServices(
      await readServices(await this.where()),
      namespaceOf(process.env),
      gcpSlug(cell, process.env),
    );
    return [...this.outputFor(cell), services].join("\n");
  }

  async destroy(cell: CellUnderTest): Promise<void> {
    const dir = await cellTree(cell);
    try {
      await this.run(
        cell,
        dir,
        "destroy",
        "destroy",
        ["destroy", "production", "--yes"],
        childEnv(dir),
      );
    } finally {
      this.output.delete(cell.slug);
      await rm(treeRoot(cell, "gcp"), { recursive: true, force: true });
    }
  }

  private outputFor(cell: CellUnderTest): string[] {
    let output = this.output.get(cell.slug);
    if (!output) {
      output = [];
      this.output.set(cell.slug, output);
    }
    return output;
  }

  async previewUp(cell: CellUnderTest, name: string): Promise<PreviewRelease[]> {
    const dir = await cellTree(cell);
    const attempt = (this.previewsUp.get(cell.slug) ?? 0) + 1;
    this.previewsUp.set(cell.slug, attempt);
    const ran = await this.run(
      cell,
      dir,
      "preview",
      `preview-up-${attempt}`,
      ["preview", "up", name, "--yes", "--json"],
      childEnv(dir),
    );
    const record = await readFile(path.join(dir, ".ocel", "deploy-report.json"), "utf8");
    return previewReleasesIn(ran.stdout, record, `ocel preview up ${name}`);
  }

  async previewPrune(cell: CellUnderTest, name: string, keep: number): Promise<void> {
    const dir = await cellTree(cell);
    await this.run(
      cell,
      dir,
      "preview",
      "preview-prune",
      ["preview", "prune", name, "--keep", String(keep), "--yes"],
      childEnv(dir),
    );
  }

  async previewRemove(cell: CellUnderTest, name: string): Promise<void> {
    const dir = await cellTree(cell);
    const env = childEnv(dir);
    const removed = await this.run(
      cell,
      dir,
      "preview",
      "preview-rm",
      ["preview", "rm", name, "--yes"],
      env,
    ).then(
      () => undefined,
      (error: unknown) => ({ error }),
    );
    const destroyed = await this.run(
      cell,
      dir,
      "preview",
      "preview-destroy",
      ["destroy", "preview", "--yes"],
      env,
    ).then(
      () => undefined,
      (error: unknown) => ({ error }),
    );
    if (removed || destroyed) {
      throw new Error(
        [
          ...(removed ? [`ocel preview rm ${name}: ${String(removed.error)}`] : []),
          ...(destroyed ? [`ocel destroy preview: ${String(destroyed.error)}`] : []),
        ].join("; "),
      );
    }
  }

  private run(
    cell: CellUnderTest,
    dir: string,
    phase: Phase,
    name: string,
    args: string[],
    env: NodeJS.ProcessEnv,
  ): Promise<Ran> {
    return recordOutput(this.outputFor(cell), runOcel(cell, dir, phase, name, args, env));
  }

  private async where(): Promise<Where> {
    return {
      endpoint: endpoint(),
      project: project(),
      region: region(),
      token: await accessToken(endpoint(), gcloudAccessToken),
    };
  }

  private async services() {
    return listServices(await this.where());
  }

  private async deployment(cell: CellUnderTest, phase: Phase): Promise<Deployment> {
    const found = await this.services();
    const urls =
      hostnameUrls(cell, process.env.OCEL_E2E_ZONE?.trim() || undefined) ??
      new Map(
        cell.fixture.apps.map((app) => [
          app,
          reachable(
            findAppService(found, namespaceOf(process.env), gcpSlug(cell, process.env), app).uri,
            endpoint(),
          ),
        ]),
      );
    await cell.evidence.write(
      phase,
      "deployment.json",
      `${JSON.stringify(
        {
          slug: cell.slug,
          variant: cell.variant.name,
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

  private deploys(services: Service[], project: string): boolean {
    return servicesOf(services, namespaceOf(process.env), project).length > 0;
  }

  private async deployedSlugs(): Promise<string[]> {
    const runId = currentRunIdentity();
    const found = await this.services();
    return gcpCells()
      .map((cell) => ({ cell, slug: projectSlug(cell.name, runId) }))
      .filter(({ cell, slug }) =>
        this.deploys(found, gcpSlug({ slug, fixture: cell.fixture }, process.env)),
      )
      .map(({ slug }) => slug);
  }

  private async sweepRun(runId: string): Promise<void> {
    const complaints: string[] = [];
    for (const slug of await this.deployedSlugs()) {
      const cell = cellOfSlug(gcpCells(), slug);
      const dir = await copyTree(
        fixtureDir(cell.fixture.name),
        treeDir(runId, "gcp", `sweep-${slug}`),
      );
      try {
        await writeJourneyConfig(dir, gcpSweepOverlay(cell, slug, process.env));
        if (!endpoint() && (cell.fixture.previews?.gcp ?? []).length > 0) {
          await ocel(dir, ["destroy", "preview", "--yes"], childEnv(dir));
        }
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

  private async sweepStale(runId: string): Promise<void> {
    const complaints: string[] = [];
    try {
      await this.sweepRun(runId);
    } catch (error) {
      complaints.push(error instanceof Error ? error.message : String(error));
    }
    const mine = gcpCells().map((cell) =>
      gcpSlug({ slug: projectSlug(cell.name, runId), fixture: cell.fixture }, process.env),
    );
    const found = await this.services();
    const at = await this.where();
    if (!endpoint()) {
      complaints.push(...(await this.sweepStores(at, runId)));
      complaints.push(...(await this.sweepBuckets(at, runId)));
      complaints.push(...(await this.sweepDatabases(at, runId)));
    }
    for (const name of strayServices(found, namespaceOf(process.env), mine)) {
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

  private sweptProjects(runId: string): string[] {
    return gcpCells().map((cell) =>
      sanitize(
        gcpSlug({ slug: projectSlug(cell.name, runId), fixture: cell.fixture }, process.env),
      ),
    );
  }

  private async sweepDatabases(at: Where, runId: string): Promise<string[]> {
    const mine = this.sweptProjects(runId);
    const complaints: string[] = [];
    const databases = await listDatabases(at, databaseFilter(namespaceOf(process.env)));
    for (const name of strayDatabases(databases, mine)) {
      try {
        await deleteDatabase(at, name);
        process.stdout.write(`swept ${name}\n`);
      } catch (error) {
        complaints.push(`${name}: ${String(error)}`);
      }
    }
    return complaints;
  }

  private async sweepBuckets(at: Where, runId: string): Promise<string[]> {
    const mine = this.sweptProjects(runId);
    const complaints: string[] = [];
    const buckets = await listAppBuckets(at, appBucketPrefix(namespaceOf(process.env)));
    for (const name of strayBuckets(buckets, mine)) {
      try {
        await deleteAppBucket(at, name);
        process.stdout.write(`swept ${name}\n`);
      } catch (error) {
        complaints.push(`${name}: ${String(error)}`);
      }
    }
    return complaints;
  }

  private async sweepStores(at: Where, runId: string): Promise<string[]> {
    const namespace = namespaceOf(process.env);
    const mine = this.sweptProjects(runId);
    const complaints: string[] = [];
    const stores = await listStores(at, `labels.ocel-namespace="${sanitize(namespace)}"`);
    for (const name of strayStores(stores, mine)) {
      try {
        await deleteStore(at, name);
        process.stdout.write(`swept ${name}\n`);
      } catch (error) {
        complaints.push(`${name}: ${String(error)}`);
      }
    }
    return complaints;
  }
}
