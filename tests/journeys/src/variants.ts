import type { Compute, Edge, TargetName } from "./spec";

export const BASE = "base";

export type ConfigDelta = { compute?: Compute; edge?: Edge };

export type Variant = {
  name: string;
  on?: TargetName[];
  env?: Record<string, string>;
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

export const EDGES: Variant[] = [apiGateway, cloudflare];

export const AWS: Variant[] = [container, ...EDGES];

export function runsOn(one: Variant, target: TargetName): boolean {
  return one.on === undefined || one.on.includes(target);
}
