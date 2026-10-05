import { readFile } from "node:fs/promises";
import { join } from "node:path";

export interface ProjectManifest {
  config: any;
  distDir: string;
  prerender: any;
}

export async function loadProjectManifest(projectDir: string): Promise<ProjectManifest | null> {
  let config: any;
  try {
    const serverFiles = JSON.parse(
      await readFile(join(projectDir, ".next", "required-server-files.json"), "utf8"),
    );
    config = serverFiles?.config;
  } catch {
    return null;
  }
  if (!config) return null;

  const distDir = join(projectDir, config.distDir || ".next");
  let prerender: any = null;
  try {
    prerender = JSON.parse(await readFile(join(distDir, "prerender-manifest.json"), "utf8"));
  } catch {}

  return { config, distDir, prerender };
}

export interface PrerenderWalk<R, D> {
  basePath: string;
  routes: Map<string, R>;
  dynamicRoutes: { pattern: RegExp; value: D }[];
}

export function walkPrerender<R, D>(
  manifest: ProjectManifest | null,
  readRoute: (entry: any) => R | null,
  readDynamic: (entry: any) => D | null,
): PrerenderWalk<R, D> {
  const routes = new Map<string, R>();
  for (const [path, entry] of Object.entries<any>(manifest?.prerender?.routes ?? {})) {
    const route = readRoute(entry);
    if (route) routes.set(path, route);
  }

  const dynamicRoutes: { pattern: RegExp; value: D }[] = [];
  for (const entry of Object.values<any>(manifest?.prerender?.dynamicRoutes ?? {})) {
    if (typeof entry?.routeRegex !== "string") continue;
    const value = readDynamic(entry);
    if (!value) continue;
    try {
      dynamicRoutes.push({ pattern: new RegExp(entry.routeRegex), value });
    } catch {}
  }

  return {
    basePath: typeof manifest?.config?.basePath === "string" ? manifest.config.basePath : "",
    routes,
    dynamicRoutes,
  };
}
