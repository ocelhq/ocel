import { rm } from "node:fs/promises";
import path from "node:path";
import { setTimeout as pause } from "node:timers/promises";
import { DEFAULT_BASE, writeJourneyConfig } from "../../config";
import { currentRunIdentity, projectSlug } from "../../identity";
import { fixtures as matrix } from "../../matrix/fixtures";
import { ocel, runOcel } from "../../ocel";
import { fixtureDir, treeDir } from "../../paths";
import { fixturesOn } from "../../plan";
import type { PrepareFailures } from "../../prepare";
import type { CellUnderTest } from "../../run/cellRun";
import { copyTree } from "../../tree";
import { namespaceFor, ocelEnvIn } from "./namespace";
import { cliAt, said } from "./store";
import type { AwsWorld } from "./world";

const DEFAULT_VPC_TRIES = 30;

const EVERY_FEATURE = "all";
const FLOCI_FEATURES = ["isr", "image-optimization", "cloudfront-edge", "apigateway-edge"];

const CELL_BOOTSTRAP_ARGS = ["bootstrap", "production", "--yes", "--features", EVERY_FEATURE];
export const BOOTSTRAP_DESTROY_ARGS = ["bootstrap", "destroy", "production", "--yes"];

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
      await writeJourneyConfig(dir, { base: DEFAULT_BASE, slug });
      await ocel(
        dir,
        ["bootstrap", "production", "--yes", "--features", FLOCI_FEATURES.join(",")],
        ocelEnvIn(dir),
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
      await runOcel(
        cell,
        dir,
        "deploy",
        "bootstrap",
        CELL_BOOTSTRAP_ARGS,
        ocelEnvIn(dir, namespace),
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
