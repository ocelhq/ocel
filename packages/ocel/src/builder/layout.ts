import path from "node:path";

/**
 * Build output is namespaced per app: every app owns the subtree
 * `<outDir>/apps/<name>` holding its functions, static assets, cache entries
 * and routing manifest. Nothing is shared, so two apps exposing the same route
 * cannot overwrite each other. The Go CLI discovers functions by walking these
 * subtrees, so this layout is a cross-process contract.
 */
export const APPS_DIR = "apps";

/** An app's subtree, relative to the output root. */
export function appRel(appName: string): string {
  return path.join(APPS_DIR, appName);
}

export function appOutDir(outDir: string, appName: string): string {
  return path.join(outDir, appRel(appName));
}
