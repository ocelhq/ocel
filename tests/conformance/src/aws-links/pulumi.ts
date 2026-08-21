import { access, mkdir, rm } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { run, successful } from "../process";
import type { Example } from "../types";
import { outputs } from "./outputs";

const timeoutMs = 45 * 60_000;

function pulumiEnvironment(example: Example, env: NodeJS.ProcessEnv) {
  const state = path.join(example.dir, ".ocel", "conformance-pulumi");
  const passphrase = process.env.PULUMI_CONFIG_PASSPHRASE;
  if (!passphrase) {
    throw new Error("PULUMI_CONFIG_PASSPHRASE is required by the Pulumi fixture");
  }
  return {
    state,
    env: {
      ...env,
      PULUMI_BACKEND_URL: pathToFileURL(state).href,
      PULUMI_CONFIG_PASSPHRASE: passphrase,
    },
  };
}

export async function provisionPulumi(
  example: Example,
  env: NodeJS.ProcessEnv,
  stack: string,
) {
  const local = pulumiEnvironment(example, env);
  await mkdir(local.state, { recursive: true });
  await successful("pulumi login", "pulumi", ["login", local.env.PULUMI_BACKEND_URL], {
    cwd: example.dir,
    env: local.env,
    timeoutMs,
  });
  await successful(
    "pulumi stack init",
    "pulumi",
    ["stack", "init", stack, "--non-interactive"],
    { cwd: example.dir, env: local.env, timeoutMs },
  );
  const region = process.env.AWS_REGION;
  if (!region) throw new Error("AWS_REGION is required by the Pulumi fixture");
  await successful(
    "pulumi aws region",
    "pulumi",
    ["config", "set", "aws:region", region, "--stack", stack, "--non-interactive"],
    { cwd: example.dir, env: local.env, timeoutMs },
  );
  await successful(
    "pulumi database password",
    "pulumi",
    [
      "config",
      "set",
      "--secret",
      "dbPassword",
      crypto.randomUUID().replaceAll("-", ""),
      "--stack",
      stack,
      "--non-interactive",
    ],
    { cwd: example.dir, env: local.env, timeoutMs },
  );
  await successful(
    "pulumi up",
    "pulumi",
    ["up", "--stack", stack, "--yes", "--skip-preview", "--non-interactive"],
    { cwd: example.dir, env: local.env, timeoutMs },
  );
  const result = await successful(
    "pulumi stack output",
    "pulumi",
    ["stack", "output", "--json", "--stack", stack],
    { cwd: example.dir, env: local.env, timeoutMs },
  );
  const parsed = JSON.parse(result.stdout) as Record<string, unknown>;
  return {
    outputs: outputs({
      host: parsed.host,
      port: parsed.port,
      database: parsed.database,
      subnetIds: parsed.publishedSubnetIds,
      securityGroupIds: parsed.publishedSecurityGroupIds,
    }),
  };
}

export async function removePulumi(
  example: Example,
  env: NodeJS.ProcessEnv,
  stack: string,
) {
  const local = pulumiEnvironment(example, env);
  try {
    await access(local.state);
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") return;
    throw error;
  }
  await successful("pulumi login", "pulumi", ["login", local.env.PULUMI_BACKEND_URL], {
    cwd: example.dir,
    env: local.env,
    timeoutMs,
  });
  const selected = await run(
    "pulumi",
    ["stack", "select", stack, "--non-interactive"],
    { cwd: example.dir, env: local.env, timeoutMs },
  );
  if (selected.code === 0) {
    await successful(
      "pulumi destroy",
      "pulumi",
      ["destroy", "--stack", stack, "--yes", "--skip-preview", "--non-interactive"],
      { cwd: example.dir, env: local.env, timeoutMs },
    );
    await successful(
      "pulumi stack rm",
      "pulumi",
      ["stack", "rm", stack, "--yes", "--non-interactive"],
      { cwd: example.dir, env: local.env, timeoutMs },
    );
  }
  await rm(local.state, { recursive: true, force: true });
}
