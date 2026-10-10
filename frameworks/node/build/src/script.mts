import { execFile, spawn } from "node:child_process";
import { readFileSync } from "node:fs";
import path from "node:path";
import { promisify } from "node:util";
import type { Hosting } from "@platform/edge-contract/hosting";
import { detect, resolveCommand } from "package-manager-detector";
import {
  APP_FOLDER_ENV,
  APP_NAME_ENV,
  HOSTING_FILE,
  HOSTING_VERSION,
  OUTPUT_DIR_ENV,
} from "./output.mjs";

export interface ScriptBuild {
  name: string;
  cwd: string;
  env?: Record<string, string>;
  unset?: string[];
}

const LIVE_DIR_ENV = "OCEL_LIVE_DIR";

export interface BuildNode {
  version: string;
  readsLiveDir: boolean;
}

export const buildProcess = { spawn: spawnBuild, node: readBuildNode };

async function readBuildNode(cwd: string, env: Record<string, string>): Promise<BuildNode> {
  const { stdout } = await promisify(execFile)(
    "node",
    ["-p", 'process.versions.node + " " + typeof process.getBuiltinModule'],
    { cwd, env: { ...env, PATH: prependScriptBins(cwd, env.PATH) } },
  );
  const [version = "", builtin] = stdout.trim().split(" ");
  return { version, readsLiveDir: builtin === "function" };
}

export function prependScriptBins(cwd: string, inherited = ""): string {
  const bins: string[] = [];
  for (let dir = path.resolve(cwd); ; dir = path.dirname(dir)) {
    bins.push(path.join(dir, "node_modules", ".bin"));
    if (path.dirname(dir) === dir) break;
  }
  return [...bins, inherited].filter(Boolean).join(path.delimiter);
}

export async function runBuildScript(
  app: ScriptBuild,
  owned: Record<string, string>,
  refuseBuild?: (env: Record<string, string>) => Promise<void>,
): Promise<void> {
  for (const name of Object.keys(owned)) {
    if (app.env && name in app.env) {
      throw new Error(
        `ocel: app "${app.name}" declares ${name}, which the build sets itself; rename it where it is declared`,
      );
    }
  }

  const manifest = JSON.parse(readFileSync(path.join(app.cwd, "package.json"), "utf8"));
  if (!manifest.scripts?.build) {
    throw new Error(`ocel: app "${app.name}" has no "build" script in package.json`);
  }

  const detected = await detect({ cwd: app.cwd });
  const resolved = resolveCommand(detected?.agent ?? "npm", "run", ["build"]);
  if (!resolved) throw new Error(`ocel: could not resolve a build command for app "${app.name}"`);

  const inherited = Object.fromEntries(
    Object.entries(process.env).filter(
      (entry): entry is [string, string] =>
        entry[1] !== undefined && !(app.unset ?? []).includes(entry[0]),
    ),
  );
  const env = { ...inherited, ...app.env, ...owned };
  if (app.env?.[LIVE_DIR_ENV]) {
    const node = await buildProcess.node(app.cwd, env);
    if (!node.readsLiveDir) {
      throw new Error(
        `ocel: app "${app.name}" builds with values it reads from ${LIVE_DIR_ENV}, and the SDK reads them on Node 20.16+ or 22.3+; the node its build runs in ${app.cwd} is ${node.version}`,
      );
    }
  }
  await refuseBuild?.(env);
  await buildProcess.spawn(resolved.command, resolved.args, app.cwd, env);
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
      env,
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

export interface OutputBuild extends ScriptBuild {
  outputDir: string;
  folder?: string;
}

export interface Adapter {
  framework: string;
  name: string;
  setup: string;
  upgrade: string;
  refuseBuild?: (env: Record<string, string>) => Promise<void>;
}

export async function runAdapterBuild(
  app: OutputBuild,
  adapter: Adapter,
  env: Record<string, string>,
): Promise<Hosting> {
  await runBuildScript(
    app,
    {
      NODE_ENV: "production",
      [APP_NAME_ENV]: app.name,
      [OUTPUT_DIR_ENV]: app.outputDir,
      [APP_FOLDER_ENV]: app.folder ?? "",
      ...env,
    },
    adapter.refuseBuild,
  );
  return readHosting(app, adapter);
}

function readHosting(app: OutputBuild, adapter: Adapter): Hosting {
  const refuseUnadapted = (why: string) =>
    new Error(
      `ocel: app "${app.name}" built, but ${why}, so its build did not run through ${adapter.name}. ${adapter.setup}`,
    );
  let parsed: unknown;
  try {
    parsed = JSON.parse(readFileSync(path.join(app.outputDir, HOSTING_FILE), "utf8"));
  } catch (err) {
    if ((err as NodeJS.ErrnoException).code === "ENOENT") {
      throw refuseUnadapted(`nothing wrote ${HOSTING_FILE} to ${app.outputDir}`);
    }
    throw err;
  }
  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
    throw refuseUnadapted(`the ${HOSTING_FILE} it wrote is not an object`);
  }
  const hosting = parsed as Partial<Hosting>;
  if (hosting.framework !== adapter.framework) {
    throw refuseUnadapted(
      `the ${HOSTING_FILE} it wrote names framework ${JSON.stringify(hosting.framework)}`,
    );
  }
  if (hosting.version !== HOSTING_VERSION) {
    throw new Error(
      `ocel: app "${app.name}" built, but ${adapter.name} wrote ${HOSTING_FILE} version ${JSON.stringify(hosting.version)}, and this CLI reads version ${HOSTING_VERSION}. ${adapter.upgrade}`,
    );
  }
  return hosting as Hosting;
}
