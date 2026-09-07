import { rm } from "node:fs/promises";
import path from "node:path";
import { HARNESS_PREFIX } from "../../identity";
import { workTree } from "../../ocel";
import { fixtureDir, treeDir } from "../../paths";
import type { LadderHooks } from "../../spec";
import { copyTree } from "../../tree";
import { recordPlacement, refuse } from "./ladder";
import { place } from "./place";
import { spawnBin } from "./run";
import { type Cli, cliAt } from "./store";

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

async function deployEnv(): Promise<NodeJS.ProcessEnv> {
  const where = await place();
  return where.endpoint ? { ...process.env, AWS_ENDPOINT_URL: where.endpoint } : { ...process.env };
}

export const sstHooks: LadderHooks = {
  refuse,

  async beforeUp(cell) {
    const dir = await workTree(cell, "aws");
    const stage = `j-${cell.runId}`;
    const bin = path.join(dir, "node_modules", ".bin", "sst");
    const result = await spawnBin(bin, ["deploy", "--stage", stage], dir, await deployEnv());
    await cell.evidence.write("up", "sst-deploy.stdout", result.stdout);
    const outputs = parseSstOutputs(result.stdout);
    recordPlacement(cell.slug, {
      subnetIds: (outputs.subnetIds ?? "").split(",").filter(Boolean),
      securityGroupIds: (outputs.securityGroupIds ?? "").split(",").filter(Boolean),
    });
  },

  async afterDestroy(cell) {
    const dir = await workTree(cell, "aws");
    const stage = `j-${cell.runId}`;
    const bin = path.join(dir, "node_modules", ".bin", "sst");
    await spawnBin(bin, ["remove", "--stage", stage], dir, await deployEnv());
  },
};

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

export async function sstSweep(runId: string): Promise<void> {
  const where = await place();
  const stages = new Set([
    `${HARNESS_PREFIX}${runId}`,
    ...(await recordedStages(cliAt(where.endpoint))),
  ]);
  const dir = await copyTree(
    fixtureDir("sdk/with-sst"),
    treeDir(runId, "aws", "ladder-sweep-with-sst"),
  );
  try {
    const bin = path.join(dir, "node_modules", ".bin", "sst");
    for (const stage of stages) {
      try {
        await spawnBin(bin, ["remove", "--stage", stage], dir, await deployEnv());
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
