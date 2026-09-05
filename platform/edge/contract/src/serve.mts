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
  runtime: string;
  buildId: string;
  edgeRouting: boolean;
  entry: string;
  needs: Partial<Record<Need, NeedDetail>>;
};
