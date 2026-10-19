import { ocel } from "./ocel";
import type { Log } from "./progress";

export type ProjectTier = "production" | "preview";

type Envelope = {
  ok?: boolean;
  data?: { projects?: Array<{ slug?: unknown }> };
  error?: { message?: string };
};

export function projectSlugs(stdout: string): string[] {
  let envelope: Envelope;
  try {
    envelope = JSON.parse(stdout) as Envelope;
  } catch {
    throw new Error(`ocel projects --json printed no JSON: ${stdout.trim() || "(nothing)"}`);
  }
  if (envelope.ok !== true) {
    throw new Error(
      `ocel projects --json failed: ${envelope.error?.message ?? JSON.stringify(envelope)}`,
    );
  }
  const projects = envelope.data?.projects;
  if (!Array.isArray(projects)) {
    throw new Error(`ocel projects --json printed no project list: ${stdout.trim()}`);
  }
  return projects.map((project) => {
    if (typeof project.slug !== "string" || project.slug === "") {
      throw new Error(`ocel projects --json listed a project with no slug: ${stdout.trim()}`);
    }
    return project.slug;
  });
}

export function projectsDestroyArgs(tier?: ProjectTier): string[] {
  return ["projects", "destroy", ...(tier ? ["--tier", tier] : []), "--yes"];
}

export async function listProjects(
  dir: string,
  env: NodeJS.ProcessEnv,
  log?: Log,
): Promise<string[]> {
  return projectSlugs((await ocel(dir, ["projects", "--json"], env, log)).stdout);
}
