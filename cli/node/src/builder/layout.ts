import path from "node:path";

export const APPS_DIR = "apps";

export function appRel(appName: string): string {
  return path.join(APPS_DIR, appName);
}

export function appOutDir(outDir: string, appName: string): string {
  return path.join(outDir, appRel(appName));
}

export const SERVE_DESCRIPTOR_FILE = "serve.json";

export const NODE_ENTRY_ROUTE_ID = "/";

export const BUILD_PLAN_FILE = "build-plan.json";

export function functionRel(appName: string): string {
  return path.join(appRel(appName), "functions", "index.func");
}
