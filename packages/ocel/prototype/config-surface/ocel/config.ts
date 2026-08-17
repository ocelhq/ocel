/** PROTOTYPE — the config surface after edge modes (ocelhq/ocel#399). */

export interface ProviderDescriptor {
  package: string;
  options: unknown;
}

/**
 * The edge role's occupant. Config values are data, not closures — the CLI
 * `JSON.stringify`s the config — so every marker returns a descriptor.
 *
 * - omitted ⇒ `native` (the origin's own edge, CloudFront on AWS)
 * - `cfEdge()` ⇒ `cloudflare`
 * - `false` ⇒ `none` (the origin's own HTTP front, REST API Gateway on AWS)
 *
 * `native` has no marker: it is the origin's, so it is expressed by omission
 * and the origin package exports nothing under `/edge`.
 */
export interface EdgeDescriptor {
  kind: "cloudflare";
  options: unknown;
}

/** Who writes DNS records. Absent ⇒ `ocel domain` prints them and polls. */
export type DnsDescriptor =
  | { kind: "route53"; zone?: string }
  | { kind: "cloudflare"; zone?: string };

/**
 * The named needs a framework build can emit. A need the chosen edge lacks
 * refuses the deploy unless it is listed in `allowDegraded`.
 */
export type Need =
  | "edge-middleware"
  | "edge-runtime"
  | "ppr-resume"
  | "edge-cache"
  | "streaming";

export interface AppDomainConfig {
  production?: string | string[];
}

export interface ProjectDomainConfig extends AppDomainConfig {
  preview?: string;
}

export interface AppConfig {
  name: string;
  path: string;
  framework: "next" | "express" | "fastify" | "hono";
  entrypoint?: string;
  domains?: AppDomainConfig;
  folder?: string;
}

export interface OcelConfig {
  slug: string;
  links?: string[];
  discovery?: { paths?: string[] };
  provider?: ProviderDescriptor;
  edge?: EdgeDescriptor | false;
  dns?: DnsDescriptor;
  /**
   * Needs this project waives: a listed need the edge lacks degrades with a
   * notice on every deploy instead of refusing. Top-level, not inside the
   * edge marker — it states what the app can live without, which is true of
   * the project whichever edge fronts it, and `edge: false` has no marker to
   * hang it on.
   */
  allowDegraded?: Need[];
  apps?: AppConfig[];
  domains?: ProjectDomainConfig;
}

export function defineConfig(config: OcelConfig): OcelConfig {
  return config;
}

/**
 * Alternative for reaction: the one cross-field rule (`route53()` cannot pair
 * with `cfEdge()`) pulled into the types. Costs a generic `defineConfig`;
 * the runtime check in the CLI stays either way (JS callers, hand-written
 * JSON). Pick one; the plain `defineConfig` above is the recommendation.
 */
export type OcelConfigStrict<E extends OcelConfig["edge"]> = Omit<OcelConfig, "edge" | "dns"> & {
  edge?: E;
  dns?: E extends EdgeDescriptor ? Extract<DnsDescriptor, { kind: "cloudflare" }> : DnsDescriptor;
};

export function defineConfigStrict<E extends OcelConfig["edge"] = undefined>(
  config: OcelConfigStrict<E>,
): OcelConfig {
  return config as OcelConfig;
}
