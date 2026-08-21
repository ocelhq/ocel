import { run, successful } from "../process";
import type { Example } from "../types";
import { outputs } from "./outputs";

const timeoutMs = 45 * 60_000;

export async function provisionSst(
  example: Example,
  env: NodeJS.ProcessEnv,
  stack: string,
) {
  await successful(
    "sst deploy",
    "pnpm",
    ["exec", "sst", "deploy", "--stage", stack],
    { cwd: example.dir, env, timeoutMs },
  );
  const result = await successful(
    "sst outputs",
    "pnpm",
    ["exec", "sst", "outputs", "--stage", stack],
    { cwd: example.dir, env, timeoutMs },
  );
  const values: Record<string, string> = {};
  for (const line of result.stdout.split("\n")) {
    const match = /^\s*([A-Za-z][A-Za-z0-9_]*)\s*:\s*(\S.*?)\s*$/.exec(line);
    if (match) values[match[1]!] = match[2]!;
  }
  return {
    outputs: outputs({
      host: values.host,
      port: values.port,
      database: values.database,
      subnetIds: values.subnetIds,
      securityGroupIds: values.securityGroupIds,
    }),
  };
}

export async function removeSst(
  example: Example,
  env: NodeJS.ProcessEnv,
  stack: string,
) {
  const result = await run("pnpm", ["exec", "sst", "remove", "--stage", stack], {
    cwd: example.dir,
    env,
    timeoutMs,
  });
  if (
    result.code !== 0 &&
    !`${result.stdout}\n${result.stderr}`.toLowerCase().includes("not found")
  ) {
    throw new Error(`sst remove failed\n${result.stdout}\n${result.stderr}`);
  }
}
