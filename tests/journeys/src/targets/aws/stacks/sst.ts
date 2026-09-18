import { rm } from "node:fs/promises";
import path from "node:path";
import { HARNESS_PREFIX } from "../../../identity";
import { workTree } from "../../../ocel";
import { fixtureDir, treeDir } from "../../../paths";
import type { CellUnderTest } from "../../../run/cellRun";
import { copyTree } from "../../../tree";
import { spawnBin } from "../run";
import { type Cli, cliAt } from "../store";
import type { AwsWorld } from "../world";
import { AwsStack } from "./bindings";

function parseSstOutputs(stdout: string): Record<string, string> {
  const outputs: Record<string, string> = {};
  for (const line of stdout.split("\n")) {
    const match = /^\s*([A-Za-z][A-Za-z0-9_]*)\s*:\s*(\S.*?)\s*$/.exec(line);
    if (match) {
      outputs[match[1]!] = match[2]!;
    }
  }
  return outputs;
}

export async function sstEnv(
  world: Pick<AwsWorld, "endpoint">,
  env: NodeJS.ProcessEnv,
): Promise<NodeJS.ProcessEnv> {
  const endpoint = await world.endpoint();
  return endpoint ? { ...env, AWS_ENDPOINT_URL: endpoint } : { ...env };
}

export class SstStack extends AwsStack {
  async deploy(cell: CellUnderTest): Promise<void> {
    const dir = await workTree(cell, "aws");
    const stage = `j-${cell.runId}`;
    const bin = path.join(dir, "node_modules", ".bin", "sst");
    const result = await spawnBin(
      bin,
      ["deploy", "--stage", stage],
      dir,
      await sstEnv(this.world, process.env),
    );
    await cell.evidence.write("deploy", "sst-deploy.stdout", result.stdout);
    const outputs = parseSstOutputs(result.stdout);
    this.recordPlacement(cell.slug, {
      subnetIds: (outputs.subnetIds ?? "").split(",").filter(Boolean),
      securityGroupIds: (outputs.securityGroupIds ?? "").split(",").filter(Boolean),
    });
  }

  async destroy(cell: CellUnderTest): Promise<void> {
    const dir = await workTree(cell, "aws");
    const stage = `j-${cell.runId}`;
    const bin = path.join(dir, "node_modules", ".bin", "sst");
    await spawnBin(bin, ["remove", "--stage", stage], dir, await sstEnv(this.world, process.env));
  }

  async sweep(runId: string): Promise<void> {
    const stages = new Set([
      `${HARNESS_PREFIX}${runId}`,
      ...(await recordedStages(cliAt(await this.world.endpoint()))),
    ]);
    const dir = await copyTree(
      fixtureDir("sdk/with-sst"),
      treeDir(runId, "aws", "stack-sweep-with-sst"),
    );
    try {
      const bin = path.join(dir, "node_modules", ".bin", "sst");
      for (const stage of stages) {
        try {
          await spawnBin(
            bin,
            ["remove", "--stage", stage],
            dir,
            await sstEnv(this.world, process.env),
          );
        } catch (error) {
          if (!isStageNotFound(error)) {
            throw error;
          }
        }
      }
    } finally {
      await rm(dir, { recursive: true, force: true });
    }
  }
}

function isStageNotFound(error: unknown): boolean {
  return error instanceof Error && /Stage not found/.test(error.message);
}

export function harnessStagesIn(stateKeys: string[]): string[] {
  return stateKeys
    .map((key) => path.basename(key, ".json"))
    .filter((stage) => stage.startsWith(HARNESS_PREFIX));
}

async function recordedStages(cli: Cli): Promise<string[]> {
  let bootstrap: string;
  try {
    bootstrap = await cli([
      "ssm",
      "get-parameter",
      "--name",
      "/sst/bootstrap",
      "--query",
      "Parameter.Value",
      "--output",
      "text",
    ]);
  } catch (error) {
    if (/ParameterNotFound/.test(String(error))) {
      return [];
    }
    throw error;
  }
  const { state } = JSON.parse(bootstrap) as { state?: string };
  if (!state) {
    return [];
  }
  const raw = await cli([
    "s3api",
    "list-objects-v2",
    "--bucket",
    state,
    "--prefix",
    "app/with-sst/",
    "--query",
    "Contents[].Key",
    "--output",
    "json",
  ]);
  return harnessStagesIn((JSON.parse(raw) as string[] | null) ?? []);
}
