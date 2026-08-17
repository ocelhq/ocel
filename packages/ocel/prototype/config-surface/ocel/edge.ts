/** PROTOTYPE — `ocel/edge` (ocelhq/ocel#399). */
import type { EdgeDescriptor } from "ocel/config";

/**
 * Nothing at release: the API token and account id stay in the environment
 * (`CLOUDFLARE_API_TOKEN`, `CLOUDFLARE_ACCOUNT_ID`) as they do today, since
 * a config file is committed and neither belongs in git. Kept as an object
 * so a later option lands without a signature change.
 */
export interface CloudflareEdgeOptions {}

/** Cloudflare Workers play the edge role: full Next parity. */
export function cfEdge(options: CloudflareEdgeOptions = {}): EdgeDescriptor {
  return { kind: "cloudflare", options };
}
