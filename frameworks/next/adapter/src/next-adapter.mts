import { basename, join } from "node:path";
import { refuseAppCacheHandlers } from "@framework/next-cache/app-cache-handlers";
import { BuildOutput } from "@framework/node-build/output";
import type { NextAdapter } from "next";
import { PHASE_PRODUCTION_BUILD } from "next/constants.js";
import {
  type BuildContext,
  type BuildPlan,
  planBuild,
  warnInertEdgePrerenders,
} from "./build-plan.mjs";
import { emitCacheEntries, emitFetchEntries } from "./cache-seed.mjs";
import { buildRouteTable, warnOriginMiddlewareBlastRadius } from "./dispatch-table.mjs";
import { emitEdgeBundle } from "./edge-bundle.mjs";
import { packFunctions, writeFunctionBundles } from "./function-bundles.mjs";
import { hostingNeeds } from "./hosting-needs.mjs";
import { serializeImageConfig } from "./image-config.mjs";
import { withPublicValues } from "./public-values.mjs";
import { deriveNextStatic, servedPathname, writeStaticAssets } from "./static-assets.mjs";

function writeHosting(
  ctx: BuildContext,
  plan: BuildPlan,
  output: BuildOutput,
  rootFunction: string,
) {
  output.writeHosting({
    framework: "next",
    frameworkBuildId: ctx.buildId,
    rootFunction,
    routeTable: "next",
    static: deriveNextStatic(ctx.outputs.staticFiles.map(servedPathname)),
    needs: hostingNeeds(ctx, plan),
  });
}

function edgeBundleSources(plan: BuildPlan) {
  if (!plan.programmableEdge) return [];
  return [...plan.edgeRoutes, ...(plan.edgeMiddleware ? [plan.edgeMiddleware] : [])];
}

const adapter = {
  name: "ocel-adapter",

  async modifyConfig(base, { phase }) {
    const config = withPublicValues(base);
    if (phase === PHASE_PRODUCTION_BUILD) {
      refuseAppCacheHandlers(config);
      return {
        ...config,
        cacheMaxMemorySize: 0,
        experimental: { ...config.experimental, trustHostHeader: true },
      };
    }
    return config;
  },

  async onBuildComplete(ctx) {
    const plan = planBuild(ctx);
    warnInertEdgePrerenders(ctx, plan);

    const output = BuildOutput.fromEnv({
      dir: join(process.cwd(), ".ocel/output"),
      app: basename(ctx.projectDir),
    });

    const functions = await packFunctions(ctx, plan);
    await writeFunctionBundles(ctx, plan, functions, output);

    const statics = await writeStaticAssets(ctx, plan, output);
    warnOriginMiddlewareBlastRadius(ctx, plan, statics);

    await emitCacheEntries(output.dir, plan.prerenderGroups, plan.routeKinds);
    await emitFetchEntries(output.dir, ctx.distDir);

    const routeTable = buildRouteTable(ctx, plan, functions, statics, output.app);
    output.writeFile("next-route-table.json", JSON.stringify(routeTable));
    writeHosting(ctx, plan, output, functions.rootFunction);
    if (plan.images) output.writeFile("image-config.json", serializeImageConfig(plan.images));

    await emitEdgeBundle(output.dir, ctx.distDir, plan.assetSuffix, edgeBundleSources(plan));
  },
} satisfies NextAdapter;

export default adapter;
