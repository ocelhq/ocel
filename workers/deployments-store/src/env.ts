import type { DeploymentsStore } from "./deployments-do";

export interface Env {
  DEPLOYMENTS_DO: DurableObjectNamespace<DeploymentsStore>;
  // The project write-secret, minted at root-stack creation and bound as
  // secret_text on the real deploy (cloud/edge/cloudflare/cloudflare.go
  // scriptBindings). The plaintext default in wrangler.jsonc is a local
  // dev/test convenience only.
  WRITE_SECRET: string;
}
