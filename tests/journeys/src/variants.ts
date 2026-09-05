import type { Compute, Edge, Suite, TargetName } from "./spec";

export const SUPPRESS_RESOURCES_ENV = "OCEL_DEPLOY_SUPPRESS_RESOURCES";

export const BASE = "base";

export type ConfigDelta = { compute?: Compute; edge?: Edge };

export type Variant = {
  name: string;
  on?: TargetName[];
  suites?: Suite[];
  env?: Record<string, string>;
  config?: ConfigDelta;
};

function variant(name: string, shape: Omit<Variant, "name">): Variant {
  if (name === BASE || !/^[a-z][a-z0-9-]*$/.test(name)) {
    throw new Error(`${name} is no variant name: lowercase, dashes, and never ${BASE}`);
  }
  return { name, ...shape };
}

function both<T extends string>(one: T[] | undefined, other: T[] | undefined): T[] | undefined {
  if (one === undefined || other === undefined) {
    return one ?? other;
  }
  return one.filter((entry) => other.includes(entry));
}

export function compose(first: Variant, second: Variant): Variant {
  const on = both(first.on, second.on);
  const suites = both(first.suites, second.suites);
  return variant(`${first.name}-${second.name}`, {
    ...(on === undefined ? {} : { on }),
    ...(suites === undefined ? {} : { suites }),
    ...(first.env || second.env ? { env: { ...first.env, ...second.env } } : {}),
    ...(first.config || second.config ? { config: { ...first.config, ...second.config } } : {}),
  });
}

export const hello = variant("hello", {
  on: ["aws", "vps"],
  suites: ["health", "static"],
  env: { [SUPPRESS_RESOURCES_ENV]: "1" },
});

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

export const helloApiGateway = compose(hello, apiGateway);

export const EDGES: Variant[] = [apiGateway, cloudflare];

export const AWS: Variant[] = [container, ...EDGES];

export function runsOn(one: Variant, target: TargetName): boolean {
  return one.on === undefined || one.on.includes(target);
}
