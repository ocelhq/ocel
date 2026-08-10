import path from "node:path";

export const APPS_DIR = "apps";

export function appRel(appName: string): string {
  return path.join(APPS_DIR, appName);
}

export function appOutDir(outDir: string, appName: string): string {
  return path.join(outDir, appRel(appName));
}
