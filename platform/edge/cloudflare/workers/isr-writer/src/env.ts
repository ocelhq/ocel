import type { IsrDeploy } from "./isr-deploy";
import type { IsrSnapshot } from "./isr-snapshot";

export interface Env {
  ISR_WRITER_DO: DurableObjectNamespace<IsrDeploy>;
  ISR_SNAPSHOT_DO: DurableObjectNamespace<IsrSnapshot>;
  OCEL_CACHE_STORE: R2Bucket;
  BOOTSTRAP_SECRET: string;
}
