import { readFileSync } from "node:fs";
import path from "node:path";
import { detect, resolveCommand } from "package-manager-detector";
import type { AppInput, BuildOptions, FunctionSummary } from "./types.js";

/**
 * Test seam over the actual build spawn. Mirrors the Go builderExec pattern:
 * tests replace it to assert the resolved command without running `next build`.
 */
export const nextRunner = { run: spawnBuild };

/**
 * Build a Next app by running its own build script. Emits no `.func` yet:
 * assembling the Next standalone output into a deployable Lambda artifact is a
 * separate follow-up, so this proves the path and returns nothing.
 */
export async function buildNext(input: AppInput, _options: BuildOptions): Promise<FunctionSummary[]> {
  const pkg = JSON.parse(readFileSync(path.join(input.cwd, "package.json"), "utf8"));
  if (!pkg.scripts?.build) {
    throw new Error(`ocel: app "${input.name}" has no "build" script in package.json`);
  }

  const detected = await detect({ cwd: input.cwd });
  const cmd = resolveCommand(detected?.agent ?? "npm", "run", ["build"]);
  if (!cmd) throw new Error(`ocel: could not resolve a build command for app "${input.name}"`);

  await nextRunner.run(cmd.command, cmd.args, input.cwd);
  process.stderr.write(`ocel: Next app "${input.name}" built\n`);
  return [];
}

async function spawnBuild(command: string, args: string[], cwd: string): Promise<void> {
  const { spawn } = await import("node:child_process");
  await new Promise<void>((resolve, reject) => {
    const child = spawn(command, args, { cwd, stdio: ["ignore", "inherit", "inherit"] });
    child.on("error", reject);
    child.on("exit", (code) =>
      code === 0 ? resolve() : reject(new Error(`${command} ${args.join(" ")} exited with code ${code}`)),
    );
  });
}
