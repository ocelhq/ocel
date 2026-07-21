import type { DeploymentsStore } from "./deployments-do";

export interface Env {
  DEPLOYMENTS_DO: DurableObjectNamespace<DeploymentsStore>;
  // The account-level bootstrap credential, minted once when the shared store
  // worker is provisioned at bootstrap and bound as secret_text on the real
  // deploy (cloud/edge/cloudflare bootstrap). It authorizes exactly one op —
  // initializing/rotating a project's DO instance; every other op
  // authenticates against that instance's own stored project secret. The
  // plaintext default in wrangler.jsonc is a local dev/test convenience only.
  BOOTSTRAP_SECRET: string;
}
