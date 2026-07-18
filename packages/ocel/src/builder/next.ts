import { readFileSync } from "node:fs";
import path from "node:path";
import { detect, resolveCommand } from "package-manager-detector";
import { appOutDir } from "./layout.js";
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
export async function buildNext(input: AppInput, options: BuildOptions): Promise<FunctionSummary[]> {
  const pkg = JSON.parse(readFileSync(path.join(input.cwd, "package.json"), "utf8"));
  if (!pkg.scripts?.build) {
    throw new Error(`ocel: app "${input.name}" has no "build" script in package.json`);
  }

  const detected = await detect({ cwd: input.cwd });
  const cmd = resolveCommand(detected?.agent ?? "npm", "run", ["build"]);
  if (!cmd) throw new Error(`ocel: could not resolve a build command for app "${input.name}"`);

  // Both reach the Next adapter, which runs inside `next build` with the app
  // dir as its cwd and so can infer neither. OCEL_APP_NAME is recorded in
  // routing-manifest.json so the deploy path can key this app's prerender
  // assets in the account-global asset bucket; OCEL_OUTPUT_DIR is the app's own
  // subtree of the build output, which is what keeps two Next apps apart.
  await nextRunner.run(cmd.command, cmd.args, input.cwd, {
    OCEL_APP_NAME: input.name,
    OCEL_OUTPUT_DIR: appOutDir(options.outDir, input.name),
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
