import type { Compute, Edge, TargetName } from "./spec";

export const BASE = "base";

export type ConfigDelta = { compute?: Compute; edge?: Edge };

export type Variant = {
  name: string;
  on?: TargetName[];
  config?: ConfigDelta;
};

function variant(name: string, shape: Omit<Variant, "name">): Variant {
  if (name === BASE || !/^[a-z][a-z0-9-]*$/.test(name)) {
    throw new Error(`${name} is no variant name: lowercase, dashes, and never ${BASE}`);
  }
  return { name, ...shape };
}

export const container = variant("container", {
  on: ["aws"],
  config: { compute: "container" },
});

export const apiGateway = variant("api-gateway", {
  on: ["aws"],
  config: { edge: "api-gateway" },
});

export const cloudflare = variant("cloudflare", {
  on: ["aws"],
  config: { edge: "cloudflare" },
});

export const NEXT_VARIANTS: Variant[] = [container, cloudflare];

export const HTTP_VARIANTS: Variant[] = [container, apiGateway];

export function runsOn(one: Variant, target: TargetName): boolean {
  return one.on === undefined || one.on.includes(target);
}
