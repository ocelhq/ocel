import { randomBytes } from "node:crypto";
import { mkdir, rm } from "node:fs/promises";
import path from "node:path";
import { copyTree, workTree } from "../../ocel";
import { exampleDir, outputRoot, treeDir } from "../../paths";
import type { LadderHooks } from "../../spec";
import { recordPlacement, refuse } from "./ladder";
import { place } from "./place";
import { spawnBin } from "./run";

const EMULATED_SERVICES = [
  "cloudformation",
  "dynamodb",
  "ec2",
  "iam",
  "kms",
  "rds",
  "s3",
  "secretsmanager",
  "sts",
];

async function pulumiEnv(runId: string): Promise<NodeJS.ProcessEnv> {
  const where = await place();
  const env: NodeJS.ProcessEnv = {
    ...process.env,
    PULUMI_CONFIG_PASSPHRASE: "journey",
  };
  if (where.world === "floci") {
    const state = path.join(outputRoot, runId, "aws", "pulumi-state");
    await mkdir(state, { recursive: true });
    env.PULUMI_BACKEND_URL = `file://${state}`;
  }
  return env;
}

async function pulumi(dir: string, args: string[], env: NodeJS.ProcessEnv): Promise<string> {
  const result = await spawnBin("pulumi", args, dir, env);
  return result.stdout;
}

async function stackExists(dir: string, stack: string, env: NodeJS.ProcessEnv): Promise<boolean> {
  const raw = await pulumi(dir, ["stack", "ls", "--json"], env);
  const stacks = JSON.parse(raw) as Array<{ name?: string }>;
  return stacks.some((row) => row.name === stack);
}

async function configureStack(dir: string, stack: string, env: NodeJS.ProcessEnv): Promise<void> {
  await pulumi(dir, ["stack", "select", stack, "--create"], env);
  await pulumi(dir, ["config", "set", "aws:region", "us-east-1"], env);
  await pulumi(
    dir,
    ["config", "set", "--secret", "dbPassword", randomBytes(16).toString("hex")],
    env,
  );
  const where = await place();
  if (!where.endpoint) {
    return;
  }
  await pulumi(dir, ["config", "set", "aws:skipCredentialsValidation", "true"], env);
  await pulumi(dir, ["config", "set", "aws:s3UsePathStyle", "true"], env);
  for (const [index, service] of EMULATED_SERVICES.entries()) {
    await pulumi(dir, ["config", "set", "--path", `aws:endpoints[${index}].${service}`, where.endpoint], env);
  }
}

export const pulumiHooks: LadderHooks = {
  refuse,

  async beforeUp(cell) {
    const dir = await workTree(cell, "aws");
    const stack = `j-${cell.runId}`;
    const env = await pulumiEnv(cell.runId);
    await configureStack(dir, stack, env);
    const stdout = await pulumi(dir, ["up", "--yes"], env);
    await cell.evidence.write("up", "pulumi-up.stdout", stdout);
    const outputs = JSON.parse(await pulumi(dir, ["stack", "output", "--json"], env)) as Record<
      string,
      unknown
    >;
    recordPlacement(cell.slug, {
      subnetIds: (outputs.networkSubnetIds as string[] | undefined) ?? [],
      securityGroupIds: (outputs.networkSecurityGroupIds as string[] | undefined) ?? [],
    });
  },

  async afterDestroy(cell) {
    const dir = await workTree(cell, "aws");
    const stack = `j-${cell.runId}`;
    const env = await pulumiEnv(cell.runId);
    await pulumi(dir, ["stack", "select", stack], env);
    await pulumi(dir, ["destroy", "--yes"], env);
    await pulumi(dir, ["stack", "rm", "--yes"], env);
  },
};

export async function pulumiSweep(runId: string): Promise<void> {
  const dir = await copyTree(
    exampleDir("with-pulumi"),
    treeDir(runId, "aws", "ladder-sweep-with-pulumi"),
  );
  try {
    const stack = `j-${runId}`;
    const env = await pulumiEnv(runId);
    if (!(await stackExists(dir, stack, env))) {
      return;
    }
    await pulumi(dir, ["stack", "select", stack], env);
    await pulumi(dir, ["destroy", "--yes"], env);
    await pulumi(dir, ["stack", "rm", "--yes"], env);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
}
