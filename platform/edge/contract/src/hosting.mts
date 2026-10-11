export type Need = "edge-middleware" | "edge-runtime" | "ppr-resume" | "edge-cache" | "streaming";

export type NeedDetail = {
  count: number;
  routes?: string[];
  matchers?: string[];
};

export type RouteTableFormat = "next";

export type Static = {
  immutablePrefixes: string[];
  mustRevalidatePrefixes?: string[];
};

export type CacheRule = {
  path: string;
  revalidate: number;
  expire?: number;
};

export type Hosting = {
  version: 1;
  framework: string;
  frameworkBuildId: string;
  rootFunction: string;
  routeTable?: RouteTableFormat;
  static?: Static;
  isr?: boolean;
  cache?: CacheRule[];
  needs: Partial<Record<Need, NeedDetail>>;
};
