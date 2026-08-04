import type { IsrDeploy } from "./isr-deploy";
import type { IsrSnapshot } from "./isr-snapshot";

// OCEL_CACHE_STORE is the substrate's cache bucket, bound natively — the whole
// reason this worker exists. BOOTSTRAP_SECRET is the account-level credential
// bound as secret_text when the worker is provisioned; the plaintext default in
// wrangler.jsonc is a local dev/test convenience only.
export interface Env {
  ISR_WRITER_DO: DurableObjectNamespace<IsrDeploy>;
  ISR_SNAPSHOT_DO: DurableObjectNamespace<IsrSnapshot>;
  OCEL_CACHE_STORE: R2Bucket;
  BOOTSTRAP_SECRET: string;
}
