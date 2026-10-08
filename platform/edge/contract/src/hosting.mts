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

export type Hosting = {
  version: 1;
  framework: string;
  frameworkBuildId: string;
  rootFunction: string;
  routeTable?: RouteTableFormat;
  static?: Static;
  needs: Partial<Record<Need, NeedDetail>>;
};
