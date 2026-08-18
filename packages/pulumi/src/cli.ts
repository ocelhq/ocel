import { spawnSync } from "node:child_process";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";

export const source = "pulumi";

export interface Target {
  project: string;
  class: "production" | "preview";
  environment?: string;
}

export function runLink(args: string[], target: Target, input?: string): void {
  checkTarget(target);
  const [runtime, entry] = ocelCommand(target.project);
  const result = spawnSync(
    runtime,
    [entry, "link", ...args, ...flagsFor(target)],
    { cwd: target.project, input, encoding: "utf8" },
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
      "an ocel project is required: it is the directory holding ocel.config.ts, whose apps consume this link, and it is never read from a Pulumi stack or project name",
    );
  }
  if (target.class !== "production" && target.class !== "preview") {
    throw new Error(
      `class ${JSON.stringify(target.class ?? null)} is neither "production" nor "preview": a link is published to an ocel class, never to a Pulumi stack name`,
    );
  }
  if (target.environment === classWideMarker) {
    throw new Error(
      `${classWideMarker} is reserved: leave the environment off to publish to the whole class, which serves every preview including the ephemeral ones`,
    );
  }
  if (target.environment && target.class !== "preview") {
    throw new Error(
      `environment ${target.environment} is named alongside class ${target.class}: a link is published to a class and, in preview, to one preview environment`,
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
    return `ocel link exited ${status ?? "on a signal"} without saying why`;
  }
  return said;
}

function ocelCommand(project: string): [string, string] {
  const require = createRequire(join(project, "ocel.config.ts"));
  let manifest: string;
  try {
    manifest = require.resolve("ocel/package.json");
  } catch (cause) {
    throw new Error(
      `@ocel/pulumi runs the ocel CLI in ${project}, and ocel is not installed there. Add ocel to that project's dependencies.`,
      { cause },
    );
  }
  return [process.execPath, join(dirname(manifest), "bin", "run.js")];
}
