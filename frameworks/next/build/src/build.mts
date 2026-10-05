import { spawn } from "node:child_process";
import { readFileSync } from "node:fs";
import path from "node:path";
import { detect, resolveCommand } from "package-manager-detector";

export interface NextBuild {
  name: string;
  cwd: string;
  outputDir: string;
  deploymentId: string;
  folder?: string;
  env?: Record<string, string>;
  edgeKind?: string;
  allowDegraded?: string[];
  nextRuntimeDir?: string;
  maxFunctionBytes?: number;
}

const ADAPTER_PATH_ENV = "NEXT_ADAPTER_PATH";
const DEPLOYMENT_ID_ENV = "NEXT_DEPLOYMENT_ID";

export const buildProcess = { spawn: spawnBuild };

export async function buildNext(app: NextBuild, adapterPath: string): Promise<void> {
  for (const owned of [ADAPTER_PATH_ENV, DEPLOYMENT_ID_ENV]) {
    if (app.env && owned in app.env) {
      throw new Error(
        `ocel: a variable is declared as ${owned}, which the build environment owns; rename it where it is declared`,
      );
    }
  }

  const pkg = JSON.parse(readFileSync(path.join(app.cwd, "package.json"), "utf8"));
  if (!pkg.scripts?.build) {
    throw new Error(`ocel: app "${app.name}" has no "build" script in package.json`);
  }

  const detected = await detect({ cwd: app.cwd });
  const cmd = resolveCommand(detected?.agent ?? "npm", "run", ["build"]);
  if (!cmd) throw new Error(`ocel: could not resolve a build command for app "${app.name}"`);

  await buildProcess.spawn(cmd.command, cmd.args, app.cwd, {
    ...app.env,
    NODE_ENV: "production",
    OCEL_APP_NAME: app.name,
    OCEL_OUTPUT_DIR: app.outputDir,
    OCEL_APP_FOLDER: app.folder ?? "",
    OCEL_EDGE_KIND: app.edgeKind ?? "",
    OCEL_ALLOW_DEGRADED: (app.allowDegraded ?? []).join(","),
    OCEL_NEXT_RUNTIME_DIR: app.nextRuntimeDir ?? "",
    OCEL_MAX_FUNCTION_BYTES: app.maxFunctionBytes ? String(app.maxFunctionBytes) : "",
    [ADAPTER_PATH_ENV]: adapterPath,
    [DEPLOYMENT_ID_ENV]: app.deploymentId,
  });
  process.stderr.write(`ocel: Next app "${app.name}" built\n`);
}

async function spawnBuild(
  command: string,
  args: string[],
  cwd: string,
  env: Record<string, string>,
): Promise<void> {
  await new Promise<void>((resolve, reject) => {
    const child = spawn(command, args, {
      cwd,
      env: { ...process.env, ...env },
      stdio: ["ignore", "inherit", "inherit"],
    });
    child.on("error", reject);
    child.on("exit", (code) =>
      code === 0
        ? resolve()
        : reject(new Error(`${command} ${args.join(" ")} exited with code ${code}`)),
    );
  });
}
