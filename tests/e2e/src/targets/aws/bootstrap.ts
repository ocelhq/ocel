import { rm } from "node:fs/promises";
import path from "node:path";
import { setTimeout as pause } from "node:timers/promises";
import { writeJourneyConfig } from "../../config";
import { currentRunIdentity, projectSlug } from "../../identity";
import { fixtures as matrix } from "../../matrix/fixtures";
import type { Variant } from "../../matrix/types";
import { NonzeroExitError, ocel, type Ran, runOcel } from "../../ocel";
import { fixtureDir, treeDir } from "../../paths";
import { fixturesOn } from "../../plan";
import type { PrepareFailures } from "../../prepare";
import type { CellUnderTest } from "../../run/cellRun";
import { copyTree } from "../../tree";
import { namespaceFor, ocelEnvIn } from "./namespace";
import { cliAt, said } from "./store";
import type { AwsWorld } from "./world";

const DEFAULT_VPC_TRIES = 30;

const AWS_EDGES = ["cloudfront", "api-gateway", "cloudflare"];
const KEY_WANT = "E2E_VARIABLES_KEY_WANTED";
const VARIABLES_KEY_PART = "variables-key";

export const BOOTSTRAP_DESTROY_ARGS = ["bootstrap", "destroy", "production", "--yes"];
export const BOOTSTRAP_REFRESH_ARGS = ["bootstrap", "production", "--yes"];

export function cellBootstrapArgs(variant: Pick<Variant, "config">): string[] {
  const edge = variant.config.edge ?? "cloudfront";
  if (!AWS_EDGES.includes(edge)) {
    throw new Error(`no AWS bootstrap fronts the ${edge} edge`);
  }
  return BOOTSTRAP_REFRESH_ARGS;
}

export async function bootstrapWithVariablesKey(
  bootstrap: () => Promise<Ran>,
  run: (args: string[]) => Promise<Ran>,
): Promise<void> {
  await bootstrap();
  try {
    await run(["env", "set", `${KEY_WANT}=wanted`]);
  } catch (error) {
    if (!(error instanceof NonzeroExitError) || !refusedForTheKey(error.result)) {
      throw error;
    }
    await bootstrap();
    return;
  }
  await run(["env", "rm", KEY_WANT]);
}

function refusedForTheKey(result: Ran): boolean {
  return `${result.stdout}\n${result.stderr}`.includes(VARIABLES_KEY_PART);
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
  throw new Error(
    `the emulator never showed a default VPC, and every deploy looks one up first: ${last}`,
  );
}

export class AwsBootstrap {
  constructor(private readonly world: AwsWorld) {}

  async prepareLane(): Promise<PrepareFailures> {
    const where = await this.world.detect();
    if (where.world === "real") {
      return {};
    }
    if (where.endpoint) {
      await awaitDefaultVpc(where.endpoint);
    }
    const [first] = fixturesOn(matrix, "aws");
    if (!first) {
      throw new Error("no fixture in the matrix runs on aws, so there is nothing to bootstrap");
    }
    const runId = currentRunIdentity();
    const slug = projectSlug(path.posix.basename(first.name), runId);
    const dir = await copyTree(fixtureDir(first.name), treeDir(runId, "aws", "bootstrap"));
    try {
      await writeJourneyConfig(dir, { target: "aws", slug });
      const env = ocelEnvIn(dir);
      await bootstrapWithVariablesKey(
        () => ocel(dir, BOOTSTRAP_REFRESH_ARGS, env),
        (args) => ocel(dir, args, env),
      );
    } catch (error) {
      return { lane: error instanceof Error ? error.message : String(error) };
    } finally {
      await rm(dir, { recursive: true, force: true });
    }
    return {};
  }

  async namespaceOf(cell: CellUnderTest): Promise<string | undefined> {
    return (await this.world.real()) ? namespaceFor(cell.name, cell.runId) : undefined;
  }

  async bootstrapCell(cell: CellUnderTest, dir: string): Promise<void> {
    const namespace = await this.namespaceOf(cell);
    if (namespace) {
      const env = ocelEnvIn(dir, namespace);
      await bootstrapWithVariablesKey(
        () => runOcel(cell, dir, "deploy", "bootstrap", cellBootstrapArgs(cell.variant), env),
        (args) => runOcel(cell, dir, "deploy", `variables-key-${args[1]}`, args, env),
      );
    }
  }

  async destroyCellBootstrap(cell: CellUnderTest, dir: string, namespace: string): Promise<void> {
    await runOcel(
      cell,
      dir,
      "destroy",
      "bootstrap-destroy",
      BOOTSTRAP_DESTROY_ARGS,
      ocelEnvIn(dir, namespace),
    );
  }
}
