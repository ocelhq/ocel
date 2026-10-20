import { ocel, readResultData } from "./ocel";
import type { Log } from "./progress";

export type ProjectTier = "production" | "preview";

const PROJECTS_COMMAND = "ocel projects --json";

type ListedProject = { slug?: unknown };

export function projectSlugs(stdout: string): string[] {
  const { projects } = readResultData(stdout, PROJECTS_COMMAND);
  if (!Array.isArray(projects)) {
    throw new Error(`${PROJECTS_COMMAND} printed no project list: ${stdout.trim()}`);
  }
  return projects.map((project: ListedProject) => {
    if (typeof project.slug !== "string" || project.slug === "") {
      throw new Error(`${PROJECTS_COMMAND} listed a project with no slug: ${stdout.trim()}`);
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
