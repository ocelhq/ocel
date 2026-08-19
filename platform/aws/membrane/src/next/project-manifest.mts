import { readFile } from "node:fs/promises";
import { join } from "node:path";

export interface ProjectManifest {
  config: any;
  distDir: string;
  prerender: any;
}

export async function loadProjectManifest(
  projectDir: string,
): Promise<ProjectManifest | null> {
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
    prerender = JSON.parse(
      await readFile(join(distDir, "prerender-manifest.json"), "utf8"),
    );
  } catch {}

  return { config, distDir, prerender };
}
