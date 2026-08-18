export type Need =
  | "edge-middleware"
  | "edge-runtime"
  | "ppr-resume"
  | "edge-cache"
  | "streaming";

export type NeedDetail = {
  count: number;
  routes?: string[];
  matchers?: string[];
};

export type ServeDescriptor = {
  framework: string;
  buildId: string;
  edgeRouting: boolean;
  needs: Partial<Record<Need, NeedDetail>>;
};
