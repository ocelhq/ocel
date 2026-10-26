import childProcess from "node:child_process";
import fs from "node:fs";
import nodeModule from "node:module";
import path from "node:path";

export const source = "sst";

export interface Target {
  project: string;
  tier: "production" | "preview";
  environment?: string;
}

export function runBindings(
  args: string[],
  target: Target,
  directory: string,
  input?: string,
): void {
  checkTarget(target);
  const [command, prefix] = ocelCommand(directory);
  const result = childProcess.spawnSync(
    command,
    [...prefix, "bindings", ...args, ...flagsFor(target)],
    {
      cwd: directory,
      input,
      encoding: "utf8",
    },
  );
  if (result.error) {
    throw result.error;
  }
  if (result.status !== 0) {
    throw new Error(refusal(result.stderr ?? "", result.status));
  }
}

export function checkTarget(target: Target): void {
  if (!target.project) {
    throw new Error(
      "an ocel project is required: it is the directory containing ocel.json, whose apps consume this binding, and it is never read from an SST stage or stack name",
    );
  }
  if (target.tier !== "production" && target.tier !== "preview") {
    throw new Error(
      `tier ${JSON.stringify(target.tier ?? null)} is neither "production" nor "preview": a binding is published to an ocel tier, never to a stage or stack name`,
    );
  }
  if (target.environment === tierWideMarker) {
    throw new Error(
      `${tierWideMarker} is reserved: leave the environment off to publish to the whole tier, which serves every preview that binds the name in its ocel config as "bindings": { <type>: { <name>: "@<name>" } }`,
    );
  }
  if (target.environment && target.tier !== "preview") {
    throw new Error(
      `environment ${target.environment} is named alongside tier ${target.tier}: a binding is published to a tier and, in preview, to one preview environment`,
    );
  }
}

function flagsFor(target: Target): string[] {
  const flags = target.tier === "preview" ? ["--preview"] : [];
  if (target.environment) {
    flags.push("--environment", target.environment);
  }
  return flags;
}

const tierWideMarker = "*";

function refusal(stderr: string, status: number | null): string {
  const said = stderr.trim();
  if (!said) {
    return `ocel bindings exited ${status ?? "on a signal"} without saying why`;
  }
  return said;
}

function ocelCommand(project: string): [string, string[]] {
  const named = process.env.OCEL_CLI;
  if (named) {
    return [named, []];
  }
  const found = onPath(process.platform === "win32" ? "ocel.exe" : "ocel");
  if (found) {
    return [found, []];
  }
  const bin = packageBin(project);
  if (bin) {
    return [process.execPath, [bin]];
  }
  throw new Error(
    `@ocel/sst publishes bindings with the ocel CLI and found none: set $OCEL_CLI to its path, put ocel on PATH, or add @ocel/cli to the dependencies of ${project}`,
  );
}

function onPath(file: string): string | undefined {
  for (const dir of (process.env.PATH ?? "").split(path.delimiter)) {
    if (!dir) continue;
    const candidate = path.join(dir, file);
    if (isExecutable(candidate)) {
      return candidate;
    }
  }
  return undefined;
}

function isExecutable(file: string): boolean {
  try {
    if (!fs.statSync(file).isFile()) return false;
    fs.accessSync(file, process.platform === "win32" ? fs.constants.F_OK : fs.constants.X_OK);
    return true;
  } catch {
    return false;
  }
}

function packageBin(project: string): string | undefined {
  const require = nodeModule.createRequire(path.join(project, "ocel.json"));
  let manifest: string;
  try {
    manifest = require.resolve("@ocel/cli/package.json");
  } catch {
    return undefined;
  }
  const { bin } = JSON.parse(fs.readFileSync(manifest, "utf8")) as {
    bin?: string | Record<string, string>;
  };
  const entry = typeof bin === "string" ? bin : bin?.ocel;
  return entry ? path.join(path.dirname(manifest), entry) : undefined;
}
