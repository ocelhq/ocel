import type { DeploymentsStore } from "./deployments-do";

export interface Env {
  DEPLOYMENTS_DO: DurableObjectNamespace<DeploymentsStore>;
  BOOTSTRAP_SECRET: string;
}
