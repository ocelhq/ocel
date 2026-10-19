import { copyFile, mkdir, writeFile } from "node:fs/promises";
import { dirname, join, relative, sep } from "node:path";
import { variantHeadersFile } from "@framework/next-cache/naming";
import type { LauncherConfig } from "@framework/next-protocol/launcher";
import type { BuildOutput } from "@framework/node-build/output";
import { readMaxFunctionBytes } from "./build-env.mjs";
import type { BuildContext, BuildPlan, FunctionRoute } from "./build-plan.mjs";
import { variantHeaderProjection } from "./cache-seed.mjs";
import { edgeEntryOf } from "./edge-sources.mjs";
import {
  dispatchName,
  launcherName,
  launcherRoutes,
  middlewareEntryKey,
  renderLauncher,
} from "./launcher.mjs";
import { readLauncherCache } from "./launcher-cache.mjs";
import {
  type OriginEdgePlan,
  originEdgeRuntimeName,
  planOriginEdgeEntries,
} from "./origin-edge.mjs";
import { type Bundle, type PackedMember, packBundles } from "./pack.mjs";
import { routeKeyOf } from "./route-keys.mjs";
import { traceRuntimeRequires } from "./runtime-requires.mjs";

export interface FunctionBundles {
  appRel: string;
  bundles: Bundle<FunctionRoute>[];
  missingAssets: string[];
  seededBundleId: string | undefined;
  originEdge: OriginEdgePlan | null;
  entryKeyByPathname: Map<string, string>;
  entryKeyByRouteId: Map<string, string>;
  edgeEntryByOutputId: Map<string, string>;
  rootFunction: string;
  bundleNameOf: (entryKey: string, pathname: string) => string;
}

function configClass(_route: FunctionRoute): string {
  return "";
}

function primaryEntryKey(members: readonly PackedMember<FunctionRoute>[]): string | null {
  const weighed = [...members].sort(
    (a, b) => b.sizeBytes - a.sizeBytes || (a.member.id < b.member.id ? -1 : 1),
  );
  return weighed[0]?.member.id ?? null;
}

function groupEntryRoutes(plan: BuildPlan) {
  const groups = new Map<string, FunctionRoute[]>();
  for (const route of plan.functionRoutes) {
    const key = `${route.filePath}\0${JSON.stringify(route.config)}`;
    const members = groups.get(key);
    if (members) members.push(route);
    else groups.set(key, [route]);
  }

  const entryKeyByPathname = new Map<string, string>();
  const entryKeyByRouteId = new Map<string, string>();
  const entryRoutes: FunctionRoute[] = [];
  for (const members of groups.values()) {
    members.sort(
      (a, b) => a.pathname.length - b.pathname.length || (a.pathname < b.pathname ? -1 : 1),
    );
    const entry = members[0]!;
    entryRoutes.push(entry);
    for (const m of members) {
      entryKeyByPathname.set(routeKeyOf(m, plan.basePath), entry.id);
      entryKeyByRouteId.set(m.id, entry.id);
    }
  }
  return { entryRoutes, entryKeyByPathname, entryKeyByRouteId };
}

export async function packFunctions(ctx: BuildContext, plan: BuildPlan): Promise<FunctionBundles> {
  const { projectDir, repoRoot, distDir } = ctx;
  const { basePath, nodeMiddleware, originEdgeMiddleware, originEdgeRoutes } = plan;
  const appRel = relative(repoRoot, projectDir);
  const edgeEntryByOutputId = new Map(
    plan.hostedEdgeRoutes.map((r) => [r.id, edgeEntryOf(r).entryKey]),
  );

  const { entryRoutes, entryKeyByPathname, entryKeyByRouteId } = groupEntryRoutes(plan);

  const runtimeAssets = await traceRuntimeRequires(projectDir, repoRoot);
  const assetsOf = (route: { assets: Record<string, string>; filePath: string }) => ({
    ...runtimeAssets,
    ...route.assets,
    [relative(repoRoot, route.filePath)]: route.filePath,
  });

  const originEdge = await planOriginEdgeEntries(
    distDir,
    [...originEdgeRoutes, ...(originEdgeMiddleware ? [originEdgeMiddleware] : [])],
    appRel,
    plan.assetSuffix,
    (edgeEntryKey) =>
      originEdgeMiddleware && edgeEntryKey === edgeEntryOf(originEdgeMiddleware).entryKey
        ? middlewareEntryKey
        : edgeEntryKey,
  );

  const seeded = nodeMiddleware !== undefined || originEdge !== null;
  const seedAssets = {
    ...runtimeAssets,
    ...(nodeMiddleware ? assetsOf(nodeMiddleware) : {}),
    ...(originEdge ? originEdge.seedAssets : {}),
  };

  const budgetBytes = readMaxFunctionBytes();
  const { bundles, missingAssets } = packBundles(entryRoutes, {
    entryKeyOf: (route) => route.id,
    assetsOf,
    partitionBy: configClass,
    ...(budgetBytes !== undefined && { budgetBytes }),
    ...(seeded && { seedAssets }),
  });
  const seededBundleId = seeded ? bundles[0]!.name : undefined;

  const bundleNameByEntryKey = new Map(
    bundles.flatMap((b) => b.members.map((m) => [m.member.id, b.name] as const)),
  );
  const bundleNameOf = (entryKey: string, pathname: string): string => {
    const name = bundleNameByEntryKey.get(entryKey);
    if (name === undefined) {
      throw new Error(
        `ocel: route "${pathname}" resolves to entry "${entryKey}", which no emitted bundle contains — this build cannot be served`,
      );
    }
    return name;
  };

  for (const route of originEdgeRoutes) {
    const entryKey = edgeEntryOf(route).entryKey;
    bundleNameByEntryKey.set(entryKey, seededBundleId!);
    entryKeyByPathname.set(routeKeyOf(route, basePath), entryKey);
    entryKeyByRouteId.set(route.id, entryKey);
  }

  const rootPathname = basePath || "/";
  const rootEntryKey = entryKeyByPathname.get(rootPathname);
  const rootFunction =
    rootEntryKey === undefined
      ? (bundles[0]?.name ?? "")
      : bundleNameOf(rootEntryKey, rootPathname);

  return {
    appRel,
    bundles,
    missingAssets,
    seededBundleId,
    originEdge,
    entryKeyByPathname,
    entryKeyByRouteId,
    edgeEntryByOutputId,
    rootFunction,
    bundleNameOf,
  };
}

async function readLauncherConfig(ctx: BuildContext, plan: BuildPlan): Promise<LauncherConfig> {
  const { config, distDir } = ctx;
  return {
    basePath: config.basePath || "",
    i18n: config.i18n ?? null,
    trailingSlash: !!config.trailingSlash,
    experimental: config.experimental ?? {},
    clientAssetSuffix: plan.assetSuffix,
    distDir: config.distDir || ".next",
    cache: await readLauncherCache(distDir, config),
  };
}

function entriesOf(
  ctx: BuildContext,
  bundle: Bundle<FunctionRoute>,
  plan: BuildPlan,
  functions: FunctionBundles,
): Record<string, string> {
  const relativeEntry = (filePath: string) =>
    `./${relative(ctx.projectDir, filePath).split(sep).join("/")}`;
  const entries: Record<string, string> = Object.fromEntries(
    bundle.members.map(({ member }) => [member.id, relativeEntry(member.filePath)]),
  );
  if (plan.nodeMiddleware && bundle.name === functions.seededBundleId) {
    entries[middlewareEntryKey] = relativeEntry(plan.nodeMiddleware.filePath);
  }
  return entries;
}

async function writeOriginEdgeFiles(funcDir: string, originEdge: OriginEdgePlan): Promise<void> {
  await copyFile(
    new URL("edge-node-entry.cjs", import.meta.url),
    join(funcDir, originEdgeRuntimeName),
  );
  for (const [name, body] of Object.entries(originEdge.files)) {
    const dest = join(funcDir, name);
    await mkdir(dirname(dest), { recursive: true });
    await writeFile(dest, body);
  }
}

function warnMissingAssets(missingAssets: readonly string[]): void {
  if (missingAssets.length === 0) return;
  const sample = missingAssets.slice(0, 5);
  console.warn(
    `ocel: ${missingAssets.length} traced asset(s) have no source on disk and were not copied into the bundle — a route requiring one of them fails at runtime: ${sample.join(", ")}${missingAssets.length > sample.length ? ", …" : ""}`,
  );
}

export async function writeFunctionBundles(
  ctx: BuildContext,
  plan: BuildPlan,
  functions: FunctionBundles,
  output: BuildOutput,
): Promise<void> {
  const { appRel, originEdge } = functions;
  const variantHeaders = JSON.stringify(variantHeaderProjection(plan.prerenderGroups));

  const segmentedEntryKeys = plan.prerenderGroups
    .flatMap((group) => group.segments)
    .map((segment) => functions.entryKeyByRouteId.get(segment.parentOutputId))
    .filter((entryKey): entryKey is string => entryKey !== undefined);
  const routes = launcherRoutes(
    functions.entryKeyByPathname,
    ctx.routing.dynamicRoutes ?? [],
    segmentedEntryKeys,
  );
  const launcherConfig = await readLauncherConfig(ctx, plan);

  await Promise.all(
    functions.bundles.map(async (bundle) => {
      const funcDir = output.functionDir(bundle.name);
      await output.copyIntoFunction(bundle.name, bundle.assets, ctx.repoRoot);

      const dispatchDest = join(funcDir, appRel, dispatchName);
      await mkdir(dirname(dispatchDest), { recursive: true });
      await copyFile(new URL("next-dispatch.cjs", import.meta.url), dispatchDest);

      const entries = entriesOf(ctx, bundle, plan, functions);
      if (originEdge && bundle.name === functions.seededBundleId) {
        await writeOriginEdgeFiles(join(funcDir, appRel), originEdge);
        Object.assign(entries, originEdge.entries);
      }

      const launcherRel = join(appRel, launcherName);
      await writeFile(
        join(funcDir, launcherRel),
        renderLauncher(entries, primaryEntryKey(bundle.members), routes, launcherConfig),
      );

      output.writeFunctionConfig(bundle.name, "next", launcherRel);

      await writeFile(join(funcDir, variantHeadersFile), variantHeaders);
    }),
  );

  warnMissingAssets(functions.missingAssets);
}
