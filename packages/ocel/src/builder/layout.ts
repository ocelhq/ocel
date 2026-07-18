import path from "node:path";

/**
 * Build output is namespaced per app: every app owns the subtree
 * `<outDir>/apps/<name>` holding its functions, static assets, cache entries
 * and routing manifest. Nothing is shared, so two apps exposing the same route
 * cannot overwrite each other.
 *
 * This name is a cross-process, cross-language contract with no single home:
 * this module writes the layout, cli/internal/appbuilder (appsDirName)
 * discovers functions in it, and cloud/aws/deploy/edgeworker.go (appsDirName)
 * reads each app's artifacts from it. Change one, change all three.
 */
export const APPS_DIR = "apps";

/** An app's subtree, relative to the output root. */
export function appRel(appName: string): string {
  return path.join(APPS_DIR, appName);
}

export function appOutDir(outDir: string, appName: string): string {
  return path.join(outDir, appRel(appName));
}
