import type { Hosting } from "@platform/edge-contract/hosting";
import type { BuildContext, BuildPlan } from "./build-plan.mjs";
import { isUserFacingPathname } from "./cache-seed.mjs";
import { routeKeyOf } from "./route-keys.mjs";

export function hostingNeeds(ctx: BuildContext, plan: BuildPlan): Hosting["needs"] {
  const { outputs } = ctx;
  const { middleware } = plan;
  const pprRoutes = outputs.prerenders
    .filter((p) => p.pprChain && isUserFacingPathname(p.pathname))
    .map((p) => p.pathname);
  const cachedRoutes = outputs.prerenders.filter((p) => isUserFacingPathname(p.pathname));
  const streamedRoutes = outputs.appPages.filter((p) => isUserFacingPathname(p.pathname));
  const edgeNeedRoutes = plan.edgeRoutes.filter((r) => isUserFacingPathname(r.pathname));
  return {
    ...(middleware?.runtime === "edge" && {
      "edge-middleware": {
        count: 1,
        matchers: (middleware.config.matchers ?? []).map((m) => m.sourceRegex),
      },
    }),
    ...(edgeNeedRoutes.length > 0 && {
      "edge-runtime": {
        count: edgeNeedRoutes.length,
        routes: edgeNeedRoutes.map((r) => routeKeyOf(r, plan.basePath)),
      },
    }),
    ...(pprRoutes.length > 0 && {
      "ppr-resume": { count: pprRoutes.length, routes: pprRoutes },
    }),
    ...(cachedRoutes.length > 0 && {
      "edge-cache": { count: cachedRoutes.length },
    }),
    revalidation: { count: cachedRoutes.length },
    ...(streamedRoutes.length > 0 && {
      streaming: { count: streamedRoutes.length },
    }),
  };
}
