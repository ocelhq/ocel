import { join, sep } from "node:path";
import type { CompiledImageConfig } from "@framework/next-protocol/image";
import type { AdapterOutput, NextAdapter } from "next";
import { isProgrammableEdge, readWaivedNeeds } from "./build-env.mjs";
import { groupPrerenders, type PrerenderGroup } from "./cache-seed.mjs";
import { compileImageConfig } from "./image-config.mjs";
import { clientAssetSuffix } from "./launcher.mjs";
import { nextDataPathnameOf, routeKindsById, staticRouteKeyOf } from "./route-keys.mjs";

export type BuildContext = Parameters<NonNullable<NextAdapter["onBuildComplete"]>>[0];

type Outputs = BuildContext["outputs"];

export type FunctionRoute =
  | Outputs["pages"][number]
  | Outputs["pagesApi"][number]
  | Outputs["appPages"][number]
  | Outputs["appRoutes"][number];

export interface NextDataStaticFile {
  pageKey: string;
  dataKey: string;
}

export interface BuildPlan {
  basePath: string;
  pagesDistDir: string;
  programmableEdge: boolean;
  allRoutes: FunctionRoute[];
  functionRoutes: FunctionRoute[];
  edgeRoutes: FunctionRoute[];
  originEdgeRoutes: FunctionRoute[];
  hostedEdgeRoutes: FunctionRoute[];
  middleware: AdapterOutput["MIDDLEWARE"] | undefined;
  nodeMiddleware: AdapterOutput["MIDDLEWARE"] | undefined;
  edgeMiddleware: AdapterOutput["MIDDLEWARE"] | undefined;
  originEdgeMiddleware: AdapterOutput["MIDDLEWARE"] | undefined;
  originMiddleware: AdapterOutput["MIDDLEWARE"] | undefined;
  nextDataStaticFiles: NextDataStaticFile[];
  images: CompiledImageConfig | undefined;
  assetSuffix: string;
  routeKinds: Map<string, string>;
  prerenderGroups: PrerenderGroup[];
}

export function planBuild(ctx: BuildContext): BuildPlan {
  const { outputs, distDir, config, buildId } = ctx;
  const basePath = config.basePath || "";
  const allRoutes: FunctionRoute[] = [
    ...outputs.pages,
    ...outputs.pagesApi,
    ...outputs.appPages,
    ...outputs.appRoutes,
  ];
  const pagesDistDir = join(distDir, "server", "pages") + sep;

  const { middleware } = outputs;
  const nodeMiddleware = middleware?.runtime === "nodejs" ? middleware : undefined;
  const programmableEdge = isProgrammableEdge(process.env.OCEL_EDGE_KIND);
  const waived = readWaivedNeeds();
  const compileEdgeOnOrigin = (need: string) => !programmableEdge && waived.has(need);
  const edgeMiddleware = middleware?.runtime === "edge" ? middleware : undefined;
  const originEdgeMiddleware = compileEdgeOnOrigin("edge-middleware") ? edgeMiddleware : undefined;

  const nextDataStaticFiles = middleware
    ? outputs.staticFiles
        .filter((f) => f.filePath.startsWith(pagesDistDir))
        .map((f) => {
          const pageKey = staticRouteKeyOf(f, pagesDistDir, basePath);
          return { pageKey, dataKey: nextDataPathnameOf(pageKey, buildId, basePath) };
        })
    : [];

  const images = compileImageConfig(config.images);

  const edgeRoutes = allRoutes.filter((r) => r.runtime === "edge");
  const edgeOnOrigin = compileEdgeOnOrigin("edge-runtime");

  return {
    basePath,
    pagesDistDir,
    programmableEdge,
    allRoutes,
    functionRoutes: allRoutes.filter((r) => r.runtime === "nodejs"),
    edgeRoutes,
    originEdgeRoutes: edgeOnOrigin ? edgeRoutes : [],
    hostedEdgeRoutes: edgeOnOrigin ? [] : edgeRoutes,
    middleware,
    nodeMiddleware,
    edgeMiddleware,
    originEdgeMiddleware,
    originMiddleware: nodeMiddleware ?? originEdgeMiddleware,
    nextDataStaticFiles,
    images,
    assetSuffix: clientAssetSuffix(config),
    routeKinds: routeKindsById(allRoutes, outputs.prerenders),
    prerenderGroups: groupPrerenders(outputs.prerenders),
  };
}

export function warnInertEdgePrerenders(ctx: BuildContext, plan: BuildPlan): void {
  const edgeRouteIds = new Set(plan.edgeRoutes.map((r) => r.id));
  const inertPrerenders = ctx.outputs.prerenders
    .filter((p) => edgeRouteIds.has(p.parentOutputId))
    .map((p) => p.pathname);
  if (inertPrerenders.length > 0) {
    console.warn(
      `ocel: revalidate is inert for edge-rendered route(s) ${inertPrerenders.join(", ")} — edge ISR is not supported yet, and an edge route compiled into the origin function bypasses the cache handler just the same`,
    );
  }
}
