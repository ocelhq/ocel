import { rm } from "node:fs/promises";
import { awsSweepOverlay, DEFAULT_BASE, type Overlay, writeJourneyConfig } from "../../config";
import { projectSlug, slugPart } from "../../identity";
import { fixtures as matrix } from "../../matrix/fixtures";
import type { Cell, Fixture } from "../../matrix/types";
import { ocel } from "../../ocel";
import { fixtureDir, treeDir } from "../../paths";
import { cellsOn, fixturesOn } from "../../plan";
import type { ExternalStack } from "../../stacks";
import { copyTree } from "../../tree";
import type { Sweeper } from "../types";
import { BOOTSTRAP_DESTROY_ARGS } from "./bootstrap";
import { namespaceFor, namespaceOfSlug, ocelEnvIn, strayNamespaces } from "./namespace";
import { githubRuns, livelyRuns, ofRun, runIdOf } from "./runs";
import { reclaimable, type Stranded, sweepable } from "./slugs";
import { awsStore, cliAt, namespacesStanding, type Store } from "./store";
import type { AwsWorld } from "./world";

export function cellsBySlugPart(cells: Cell[]): Map<string, Cell> {
  const byPart = new Map<string, Cell>();
  for (const cell of cells) {
    const part = slugPart(cell.name);
    const taken = byPart.get(part);
    if (taken) {
      throw new Error(
        `${cell.name} and ${taken.name} both slug to ${part}, so a sweep could not tell them apart`,
      );
    }
    byPart.set(part, cell);
  }
  return byPart;
}

export async function despite(
  complaints: string[],
  said: string,
  work: () => Promise<void>,
): Promise<void> {
  try {
    await work();
  } catch (error) {
    complaints.push(`${said}: ${String(error)}`);
  }
}

async function inFixture(
  from: string,
  runId: string,
  name: string,
  work: (dir: string) => Promise<void>,
): Promise<void> {
  const dir = await copyTree(fixtureDir(from), treeDir(runId, "aws", name));
  try {
    await work(dir);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
}

export async function sweepStacks(
  fixtures: Fixture[],
  complaints: string[],
  sweep: (stack: ExternalStack) => Promise<void>,
): Promise<void> {
  for (const fixture of fixtures) {
    const stack = fixture.stack;
    if (stack) {
      await despite(complaints, `${fixture.name} stack sweep`, () => sweep(stack));
    }
  }
}

type Busy = (names: string[]) => Promise<Set<string>>;

function busyRuns(real: boolean, complaints: string[]): Busy {
  if (!real) {
    return async () => new Set<string>();
  }
  const look = githubRuns(process.env);
  const told = new Set<string>();
  return async (names) => {
    const ids = names.flatMap((name) => runIdOf(name) ?? []);
    const { keep, unreadable } = await livelyRuns(ids, look);
    for (const { id, reason } of unreadable) {
      if (told.has(id)) {
        continue;
      }
      told.add(id);
      complaints.push(
        `run ${id} could not be read (${reason}), so its slugs and namespace were kept`,
      );
    }
    return keep;
  };
}

function underway(name: string, live: Set<string>): boolean {
  const id = runIdOf(name);
  return id !== undefined && live.has(id);
}

export function bootstrapHeldBy(namespace: string, standing: string[]): string | undefined {
  if (standing.length === 0) {
    return undefined;
  }
  return `the ${namespace} bootstrap was left standing: ${standing.join(", ")} could not be destroyed out of it`;
}

export type Swept = { slug: string; fixture: Fixture; overlay: Overlay };

export function sweepPlan(
  reclaim: Stranded[],
  byPart: Map<string, Cell>,
  fixtures: Fixture[],
  env: NodeJS.ProcessEnv,
): { swept: Swept[]; complaints: string[] } {
  const [fallback] = fixtures;
  if (!fallback) {
    const slugs = reclaim.map((stranded) => stranded.slug);
    return {
      swept: [],
      complaints:
        slugs.length > 0 ? [`no fixture runs on aws, so nothing destroys ${slugs.join(", ")}`] : [],
    };
  }
  return {
    swept: reclaim.map((stranded) => {
      const cell = stranded.cell ? byPart.get(stranded.cell) : undefined;
      return {
        slug: stranded.slug,
        fixture: cell?.fixture ?? fallback,
        overlay: awsSweepOverlay(cell, stranded.slug, env),
      };
    }),
    complaints: [],
  };
}

export class AwsSweeper implements Sweeper {
  constructor(private readonly world: AwsWorld) {}

  async list(): Promise<string[]> {
    return (await this.store()).deployedSlugs();
  }

  async exists(slug: string): Promise<boolean> {
    const real = await this.world.real();
    return (await this.store(real ? namespaceOfSlug(slug) : undefined)).exists(slug);
  }

  async sweepStale(runId: string): Promise<void> {
    const real = await this.world.real();
    const fixtures = fixturesOn(matrix, "aws");
    const cells = fixtures.flatMap((fixture) => cellsOn(fixture, "aws"));
    const byPart = cellsBySlugPart(cells);
    const mine = cells.map((cell) => projectSlug(cell.name, runId));
    const reclaim = sweepable(await this.list(), mine, [...byPart.keys()]);

    const complaints: string[] = [];
    const busy = busyRuns(real, complaints);
    const live = await busy(reclaim.map((entry) => entry.slug));
    await this.reclaimSlugs(
      runId,
      reclaim.filter((entry) => !underway(entry.slug, live)),
      byPart,
      fixtures,
      complaints,
      real,
    );

    await despite(complaints, "namespace sweep", () =>
      this.sweepNamespaces(runId, cells, byPart, complaints, busy),
    );

    await sweepStacks(fixtures, complaints, (stack) => stack.sweepStale(runId));

    await report(real, complaints);
  }

  async sweepRun(runId: string): Promise<void> {
    const where = await this.world.settle();
    if (where.world !== "real") {
      await this.sweepStale(runId);
      return;
    }
    const fixtures = fixturesOn(matrix, "aws");
    const cells = fixtures.flatMap((fixture) => cellsOn(fixture, "aws"));
    const byPart = cellsBySlugPart(cells);
    const reclaim = sweepable(ofRun(await this.list(), runId), [], [...byPart.keys()]);

    const complaints: string[] = [];
    await this.reclaimSlugs(runId, reclaim, byPart, fixtures, complaints, false);

    for (const namespace of ofRun(await namespacesStanding(cliAt(where.endpoint)), runId)) {
      await despite(complaints, `${namespace} sweep`, () =>
        this.sweepStrayNamespace(runId, namespace, byPart, complaints, false),
      );
    }

    await sweepStacks(fixtures, complaints, (stack) => stack.sweepRun(runId));

    await report(true, complaints);
  }

  private async store(namespace?: string): Promise<Store> {
    const endpoint = await this.world.endpoint();
    return namespace ? awsStore(endpoint, undefined, namespace) : awsStore(endpoint);
  }

  private async sweepStrayNamespace(
    runId: string,
    namespace: string,
    byPart: Map<string, Cell>,
    complaints: string[],
    dead: boolean,
  ): Promise<void> {
    const held = await this.store(namespace);
    const fixtures = fixturesOn(matrix, "aws");
    const stranded: Stranded[] = [];
    const standing: string[] = [];
    for (const slug of await held.deployedSlugs()) {
      const read = reclaimable(slug, [...byPart.keys()]);
      if (!read) {
        complaints.push(`${slug} stands in the ${namespace} bootstrap and no harness run made it`);
        standing.push(slug);
        continue;
      }
      stranded.push(read);
    }
    const { swept, complaints: unplanned } = sweepPlan(stranded, byPart, fixtures, process.env);
    complaints.push(...unplanned);
    for (const one of swept) {
      await inFixture(one.fixture.name, runId, `sweep-${one.slug}`, async (dir) => {
        if (dead) {
          await held.releaseLocks(one.slug);
        }
        await writeJourneyConfig(dir, one.overlay);
        await ocel(dir, ["destroy", "production", "--yes"], ocelEnvIn(dir, namespace));
        process.stdout.write(`swept ${one.slug} from the ${namespace} bootstrap\n`);
      }).catch((error) => {
        complaints.push(`${one.slug}: ${String(error)}`);
        standing.push(one.slug);
      });
    }

    const kept = bootstrapHeldBy(namespace, standing);
    if (kept) {
      complaints.push(kept);
      return;
    }

    const [first] = fixtures;
    if (!first) {
      return;
    }
    await inFixture(first.name, runId, `sweep-bootstrap-${namespace}`, async (dir) => {
      await writeJourneyConfig(dir, { base: DEFAULT_BASE, slug: namespace });
      await ocel(dir, BOOTSTRAP_DESTROY_ARGS, ocelEnvIn(dir, namespace));
      process.stdout.write(`swept the ${namespace} bootstrap\n`);
    }).catch((error) => complaints.push(`${namespace} bootstrap: ${String(error)}`));
  }

  private async sweepNamespaces(
    runId: string,
    cells: Cell[],
    byPart: Map<string, Cell>,
    complaints: string[],
    busy: Busy,
  ): Promise<void> {
    const where = await this.world.settle();
    if (where.world !== "real") {
      return;
    }
    const mine = cells.map((cell) => namespaceFor(cell.name, runId));
    const stray = strayNamespaces(await namespacesStanding(cliAt(where.endpoint)), mine);
    const live = await busy(stray);
    for (const namespace of stray.filter((name) => !underway(name, live))) {
      await despite(complaints, `${namespace} sweep`, () =>
        this.sweepStrayNamespace(runId, namespace, byPart, complaints, true),
      );
    }
  }

  private async reclaimSlugs(
    runId: string,
    reclaim: Stranded[],
    byPart: Map<string, Cell>,
    fixtures: Fixture[],
    complaints: string[],
    dead: boolean,
  ): Promise<void> {
    const { swept, complaints: unplanned } = sweepPlan(reclaim, byPart, fixtures, process.env);
    complaints.push(...unplanned);
    const store = await this.store();
    for (const one of swept) {
      await inFixture(one.fixture.name, runId, `sweep-${one.slug}`, async (dir) => {
        if (dead) {
          await store.releaseLocks(one.slug);
        }
        await writeJourneyConfig(dir, one.overlay);
        await ocel(dir, ["destroy", "production", "--yes"], ocelEnvIn(dir));
        process.stdout.write(`swept ${one.slug}\n`);
      }).catch((error) => complaints.push(`${one.slug}: ${String(error)}`));
    }

    const left = new Set(await this.list());
    for (const one of swept) {
      if (left.has(one.slug)) {
        complaints.push(`${one.slug} still stands after the sweep destroyed it`);
      }
    }
  }
}

async function report(real: boolean, complaints: string[]): Promise<void> {
  if (complaints.length === 0) {
    return;
  }
  const said = `the aws sweep left work behind:\n${complaints.join("\n")}`;
  if (real) {
    throw new Error(said);
  }
  process.stderr.write(`${said}\n`);
}
