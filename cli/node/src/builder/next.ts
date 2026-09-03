import { readFileSync } from "node:fs";
import path from "node:path";
import { detect, resolveCommand } from "package-manager-detector";
import { appOutDir } from "./layout.js";
import type { AppInput, BuildOptions, FunctionSummary } from "./types.js";

export const nextRunner = { run: spawnBuild };

export async function buildNext(input: AppInput, options: BuildOptions): Promise<FunctionSummary[]> {
  const pkg = JSON.parse(readFileSync(path.join(input.cwd, "package.json"), "utf8"));
  if (!pkg.scripts?.build) {
    throw new Error(`ocel: app "${input.name}" has no "build" script in package.json`);
  }

  const detected = await detect({ cwd: input.cwd });
  const cmd = resolveCommand(detected?.agent ?? "npm", "run", ["build"]);
  if (!cmd) throw new Error(`ocel: could not resolve a build command for app "${input.name}"`);

  await nextRunner.run(cmd.command, cmd.args, input.cwd, {
    ...input.env,
    NODE_ENV: "production",
    OCEL_APP_NAME: input.name,
    OCEL_OUTPUT_DIR: appOutDir(options.outDir, input.name),
    OCEL_APP_FOLDER: input.folder ?? "",
    OCEL_EDGE_KIND: options.edgeKind ?? "",
    OCEL_ALLOW_DEGRADED: (options.allowDegraded ?? []).join(","),
  });
  process.stderr.write(`ocel: Next app "${input.name}" built\n`);
  return [];
}

async function spawnBuild(
  command: string,
  args: string[],
  cwd: string,
  env?: Record<string, string>,
): Promise<void> {
  const { spawn } = await import("node:child_process");
  await new Promise<void>((resolve, reject) => {
    const child = spawn(command, args, {
      cwd,
      env: { ...process.env, ...env },
      stdio: ["ignore", "inherit", "inherit"],
    });
    child.on("error", reject);
    child.on("exit", (code) =>
      code === 0 ? resolve() : reject(new Error(`${command} ${args.join(" ")} exited with code ${code}`)),
    );
  });
}
