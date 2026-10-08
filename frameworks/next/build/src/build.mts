import { execFile, spawn } from "node:child_process";
import { readFileSync } from "node:fs";
import path from "node:path";
import { promisify } from "node:util";
import { detect, resolveCommand } from "package-manager-detector";

export interface NextBuild {
  name: string;
  cwd: string;
  outputDir: string;
  buildId: string;
  folder?: string;
  env?: Record<string, string>;
  unset?: string[];
  edgeKind?: string;
  allowDegraded?: string[];
  maxFunctionBytes?: number;
}

const ADAPTER_PATH_ENV = "NEXT_ADAPTER_PATH";
const DEPLOYMENT_ID_ENV = "NEXT_DEPLOYMENT_ID";
const LIVE_DIR_ENV = "OCEL_LIVE_DIR";

export interface BuildNode {
  version: string;
  readsLiveDir: boolean;
}

export const buildProcess = { spawn: spawnBuild, node: nodeOf };

async function nodeOf(cwd: string, env: Record<string, string>): Promise<BuildNode> {
  const { stdout } = await promisify(execFile)(
    "node",
    ["-p", 'process.versions.node + " " + typeof process.getBuiltinModule'],
    { cwd, env: { ...env, PATH: scriptPath(cwd, env.PATH) } },
  );
  const [version = "", builtin] = stdout.trim().split(" ");
  return { version, readsLiveDir: builtin === "function" };
}

function scriptPath(cwd: string, inherited = ""): string {
  const bins: string[] = [];
  for (let dir = path.resolve(cwd); ; dir = path.dirname(dir)) {
    bins.push(path.join(dir, "node_modules", ".bin"));
    if (path.dirname(dir) === dir) break;
  }
  return [...bins, inherited].filter(Boolean).join(path.delimiter);
}

export async function buildNext(app: NextBuild, adapterPath: string): Promise<void> {
  const owned: Record<string, string> = {
    NODE_ENV: "production",
    OCEL_APP_NAME: app.name,
    OCEL_OUTPUT_DIR: app.outputDir,
    OCEL_APP_FOLDER: app.folder ?? "",
    OCEL_EDGE_KIND: app.edgeKind ?? "",
    OCEL_ALLOW_DEGRADED: (app.allowDegraded ?? []).join(","),
    OCEL_MAX_FUNCTION_BYTES: app.maxFunctionBytes ? String(app.maxFunctionBytes) : "",
    [ADAPTER_PATH_ENV]: adapterPath,
    [DEPLOYMENT_ID_ENV]: app.buildId,
  };
  for (const name of Object.keys(owned)) {
    if (app.env && name in app.env) {
      throw new Error(
        `ocel: app "${app.name}" declares ${name}, which the build sets itself; rename it where it is declared`,
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
  await buildProcess.spawn(cmd.command, cmd.args, app.cwd, env);
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
