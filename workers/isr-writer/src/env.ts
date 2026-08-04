import type { IsrDeploy } from "./isr-deploy";

export interface Env {
  ISR_WRITER_DO: DurableObjectNamespace<IsrDeploy>;
  // The substrate's cache bucket, bound natively. It is the whole reason this
  // worker exists: with it, a deployed Lambda needs no standing R2 credentials
  // to write its ISR entries.
  OCEL_CACHE_STORE: R2Bucket;
  // The account-level bootstrap credential, minted once when the writer worker
  // is provisioned at bootstrap and bound as secret_text on the real deploy
  // (cloud/edge/cloudflare bootstrap). It authorizes exactly two ops — seeding
  // a deploy's secret hash and retiring it — and never an entry write. The
  // plaintext default in wrangler.jsonc is a local dev/test convenience only.
  BOOTSTRAP_SECRET: string;
}
