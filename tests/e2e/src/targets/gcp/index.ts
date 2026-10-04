import { type ChildProcess, execFile } from "node:child_process";
import { access, rm } from "node:fs/promises";
import path from "node:path";
import { promisify } from "node:util";
import { migrates, setsEnv, setsSecret } from "../../checks";
import {
  INITIAL_GREETING,
  JOURNEY_NONCE_ENV,
  SECRET_TOKEN,
  setsJourneyNonce,
  UNCAPPED_BODY_BYTES,
} from "../../checks/context";
import { GCP_BASE, journeyConfigIn, type Overlay, writeJourneyConfig } from "../../config";
import { currentRunIdentity, projectSlug, slugPart } from "../../identity";
import { fixtures as matrix } from "../../matrix/fixtures";
import type { Cell, Lane, Phase } from "../../matrix/types";
import { sanitize } from "../../naming";
import { configTree, ocel, type Ran, recordOutput, runOcel, treeRoot, workTree } from "../../ocel";
import { fixtureDir, laneDir, treeDir } from "../../paths";
import { cellsOn, fixturesOn } from "../../plan";
import type { PrepareFailures } from "../../prepare";
import type { CellUnderTest } from "../../run/cellRun";
import { copyTree } from "../../tree";
import { migrateCommand } from "../../workspace";
import { cloudflareUrls } from "../cloudflare";
import type { Deployment, Exposure, ReleaseCycle, Restart, Sweeper, Target } from "../types";
import { startDispatch, stopDispatch } from "./dispatch";
import {
  createTimesIn,
  deleteStore,
  KV_FEATURE,
  listStores,
  simulateMaintenance,
  storeFilter,
  strayStores,
} from "./memorystore";
import { fittedSlug, gcpSlug, namespaceOf, roomForSlug, serviceLead } from "./names";
import {
  deleteService,
  exposedServices,
  hasServicesUnder,
  listServices,
  reachable,
  readServices,
  servedBy,
  strayServices,
  switchOn,
  TASKS_FEATURE,
  type Where,
} from "./store";

const ENDPOINT_ENV = "OCEL_FLOCI_GCP_ENDPOINT";
const PROJECT_ENV = "OCEL_GCP_PROJECT";
const REGION_ENV = "OCEL_GCP_REGION";

const EMULATED_PROJECT = "floci-local";
const DEFAULT_REGION = "europe-west1";

const BRING_AN_EMULATOR_UP = [
  "scripts/floci.sh --cloud gcp create <name>",
  'eval "$(scripts/floci.sh --cloud gcp status <name>)"',
].join("\n  ");

const ran = promisify(execFile);

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

function leadsFor(slug: string, apps: string[]): string[] {
  const namespace = namespaceOf(process.env);
  const fitted = fittedSlug(slug, roomForSlug(namespace, apps));
  return apps.map((app) => serviceLead(namespace, fitted, app));
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
  return {
    base: GCP_BASE,
    slug: gcpSlug({ slug, fixture: cell.fixture }, env),
    ...cell.variant.config,
  };
}

export class GcpTarget implements Target, ReleaseCycle, Restart, Exposure {
  readonly name = "gcp";
  readonly workers = 2;
  readonly maxRequestBodyBytes = UNCAPPED_BODY_BYTES;
  readonly stepTimeoutMs = 900_000;

  private minted: Promise<string | undefined> | undefined;

  private dispatching: ChildProcess | undefined;

  private readonly output = new Map<string, string[]>();

  readonly sweeper: Sweeper = {
    list: () => this.deployedSlugs(),
    exists: async (slug) => {
      const cell = cellOfSlug(gcpCells(), slug);
      return hasServicesUnder(await this.services(), leadsFor(slug, cell.fixture.apps));
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

  async prepareLane(): Promise<PrepareFailures> {
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
        base: GCP_BASE,
        slug: projectSlug(path.posix.basename(first.name), runId),
      });
      const features = emulator ? [TASKS_FEATURE] : [KV_FEATURE, TASKS_FEATURE];
      await ocel(
        dir,
        ["bootstrap", "production", "--yes", "--features", features.join(",")],
        childEnv(dir),
      );
      if (emulator) {
        this.dispatching = await startDispatch(
          emulator,
          project(),
          region(),
          laneDir(runId, "gcp"),
        );
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
    if (migrates(cell.fixture.checks)) {
      await this.run(cell, dir, "deploy", "migrate", ["run", "--", ...migrateCommand()], env);
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
    await this.run(cell, dir, "rollback", "rollback", ["rollback"], childEnv(dir));
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
      leadsFor(cell.slug, cell.fixture.apps),
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

  private token(): Promise<string | undefined> {
    this.minted ??= (async () => {
      if (endpoint()) {
        return undefined;
      }
      const { stdout } = await ran("gcloud", ["auth", "print-access-token"]);
      return stdout.trim();
    })();
    return this.minted;
  }

  private async where(): Promise<Where> {
    return {
      endpoint: endpoint(),
      project: project(),
      region: region(),
      token: await this.token(),
    };
  }

  private async services() {
    return listServices(await this.where());
  }

  private async deployment(cell: CellUnderTest, phase: Phase): Promise<Deployment> {
    const found = await this.services();
    const leads = leadsFor(cell.slug, cell.fixture.apps);
    const urls =
      cloudflareUrls(cell, process.env.OCEL_E2E_ZONE?.trim() || undefined) ??
      new Map(
        cell.fixture.apps.map((app, at) => {
          const lead = leads[at] ?? "";
          return [app, reachable(servedBy(found, lead), endpoint())];
        }),
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

  private async deployedSlugs(): Promise<string[]> {
    const runId = currentRunIdentity();
    const found = await this.services();
    return gcpCells()
      .map((cell) => ({ cell, slug: projectSlug(cell.name, runId) }))
      .filter(({ cell, slug }) => hasServicesUnder(found, leadsFor(slug, cell.fixture.apps)))
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
    const mine = gcpCells().flatMap((cell) =>
      leadsFor(projectSlug(cell.name, runId), cell.fixture.apps),
    );
    const found = await this.services();
    const at = await this.where();
    if (!endpoint()) {
      complaints.push(...(await this.sweepStores(at, runId)));
    }
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

  private async sweepStores(at: Where, runId: string): Promise<string[]> {
    const namespace = namespaceOf(process.env);
    const mine = gcpCells().map((cell) =>
      sanitize(
        gcpSlug({ slug: projectSlug(cell.name, runId), fixture: cell.fixture }, process.env),
      ),
    );
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
