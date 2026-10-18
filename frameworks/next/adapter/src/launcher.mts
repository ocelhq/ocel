import type { LauncherConfig } from "@framework/next-protocol/launcher";

export const launcherName = "__next_launcher.cjs";

export const dispatchName = "__ocel_dispatch.cjs";

export const middlewareEntryKey = "/_middleware";

export interface LauncherRoutes {
  exact: Record<string, string>;
  dynamic: [string, string][];
  segmented: string[];
}

export function launcherRoutes(
  entryKeyByPathname: ReadonlyMap<string, string>,
  dynamicRoutes: readonly { sourceRegex: string; destination?: string }[],
  segmentedEntryKeys: readonly string[],
): LauncherRoutes {
  const exact = Object.fromEntries(entryKeyByPathname);

  const dynamic: [string, string][] = [];
  for (const route of dynamicRoutes) {
    const page = route.destination?.split("?")[0];
    if (!page || page.includes("$")) continue;
    const entryKey = entryKeyByPathname.get(page);
    if (entryKey !== undefined) dynamic.push([route.sourceRegex, entryKey]);
  }

  return { exact, dynamic, segmented: [...new Set(segmentedEntryKeys)] };
}

export function clientAssetSuffix(config: {
  deploymentId?: string;
  experimental?: { immutableAssetToken?: string };
}): string {
  const token = config.experimental?.immutableAssetToken || config.deploymentId;
  return token ? `?dpl=${token}` : "";
}

export function renderLauncher(
  entries: Record<string, string>,
  primary: string | null,
  routes: LauncherRoutes,
  nextConfig: LauncherConfig,
): string {
  return `${[
    `const { AsyncLocalStorage } = require('node:async_hooks')`,
    `globalThis.AsyncLocalStorage = AsyncLocalStorage`,
    `process.env.NODE_ENV ||= 'production'`,
    `const ENTRIES = ${JSON.stringify(entries)}`,
    `const PRIMARY = ${JSON.stringify(primary)}`,
    `const ROUTES = ${JSON.stringify(routes)}`,
    `const NEXT_CONFIG = ${JSON.stringify(nextConfig)}`,
    `module.exports = require(${JSON.stringify(`./${dispatchName}`)})({`,
    `  entries: ENTRIES,`,
    `  primary: PRIMARY,`,
    `  routes: ROUTES,`,
    `  nextConfig: NEXT_CONFIG,`,
    `  load: (specifier) => require(specifier),`,
    `})`,
  ].join("\n")}\n`;
}
