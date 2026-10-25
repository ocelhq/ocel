import type { Compute, Need } from "ocel/config";
import type { ConfigDelta, Fixture, TargetName, Variant } from "./types";

function isServed(
  target: TargetName,
  config: ConfigDelta,
  compute: Compute | undefined,
  need: Need,
) {
  if (config.image) return true;
  switch (target) {
    case "aws":
      if (config.edge === "cloudflare") return compute !== "container";
      return config.edge !== "api-gateway" && need === "edge-cache";
    case "gcp":
      if (config.edge === "cloudflare") return true;
      return config.edge === "alb" && need === "edge-cache";
    case "vps":
      return true;
    case "dev":
      return true;
  }
}

export function listWaivedNeeds(
  fixture: Pick<Fixture, "needs">,
  target: TargetName,
  variant: Pick<Variant, "config">,
): Need[] {
  const waived: Need[] = [];
  for (const [app, needs] of Object.entries(fixture.needs ?? {})) {
    const compute = variant.config.computes?.[app] ?? variant.config.compute;
    for (const need of needs) {
      if (!isServed(target, variant.config, compute, need) && !waived.includes(need)) {
        waived.push(need);
      }
    }
  }
  return waived;
}
