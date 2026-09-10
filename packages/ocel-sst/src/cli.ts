import childProcess from "node:child_process";
import nodeModule from "node:module";
import path from "node:path";

export const source = "sst";

export interface Target {
  project: string;
  class: "production" | "preview";
  environment?: string;
}

export function runBindings(args: string[], target: Target, input?: string): void {
  checkTarget(target);
  const [runtime, entry] = ocelCommand(target.project);
  const result = childProcess.spawnSync(runtime, [entry, "binding", ...args, ...flagsFor(target)], {
    cwd: target.project,
    input,
    encoding: "utf8",
  });
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
      "an ocel project is required: it is the directory holding ocel.json, whose apps consume this binding, and it is never read from an SST stage or stack name",
    );
  }
  if (target.class !== "production" && target.class !== "preview") {
    throw new Error(
      `class ${JSON.stringify(target.class ?? null)} is neither "production" nor "preview": a binding is published to an ocel class, never to a stage or stack name`,
    );
  }
  if (target.environment === classWideMarker) {
    throw new Error(
      `${classWideMarker} is reserved: leave the environment off to publish to the whole class, which serves every preview including the ephemeral ones`,
    );
  }
  if (target.environment && target.class !== "preview") {
    throw new Error(
      `environment ${target.environment} is named alongside class ${target.class}: a binding is published to a class and, in preview, to one preview environment`,
    );
  }
}

function flagsFor(target: Target): string[] {
  const flags = target.class === "preview" ? ["--preview"] : [];
  if (target.environment) {
    flags.push("--environment", target.environment);
  }
  return flags;
}

const classWideMarker = "*";

function refusal(stderr: string, status: number | null): string {
  const said = stderr.trim();
  if (!said) {
    return `ocel binding exited ${status ?? "on a signal"} without saying why`;
  }
  return said;
}

function ocelCommand(project: string): [string, string] {
  const require = nodeModule.createRequire(path.join(project, "sst.config.ts"));
  let manifest: string;
  try {
    manifest = require.resolve("ocel/package.json");
  } catch (cause) {
    throw new Error(
      `@ocel/sst runs the ocel CLI in ${project}, and ocel is not installed there. Add ocel to that project's dependencies.`,
      { cause },
    );
  }
  return [process.execPath, path.join(path.dirname(manifest), "bin", "run.js")];
}
