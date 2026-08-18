export interface ProviderDescriptor {
  package: string;
  options: unknown;
}

/**
 * The edge a project's hostnames are served from. Cloudflare is the only
 * edge you name: the origin's own edge is what you get by omitting `edge`.
 */
export interface EdgeDescriptor {
  kind: "cloudflare";
  options: unknown;
}

/** The DNS a project's records are written into. */
export interface DnsDescriptor {
  kind: string;
  zone?: string;
}

/**
 * Something an app needs of wherever it is served. A need listed in
 * `allowDegraded` is waived, and the deploy proceeds without it; an unwaived
 * need the deploy cannot meet refuses the deploy instead of silently serving
 * less than the app asks for.
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
  /**
   * Resource ids ocel binds to a link your own IaC published, instead of
   * provisioning them itself. An id left off this list provisions as usual.
   *
   * The list declares ownership and nothing else: type, properties and grants
   * all travel on the published record, and app code reads the resource the
   * same way whichever side provisioned it. It is read identically in every
   * environment — publish the same names per class.
   *
   * A listed id nothing has published refuses the deploy.
   */
  links?: string[];
  discovery?: {
    paths?: string[];
  };
  provider?: ProviderDescriptor;
  /**
   * The edge in front of the origin. Omit it and the origin serves from its
   * own edge; `false` puts nothing in front of it at all.
   */
  edge?: EdgeDescriptor | false;
  /** Where the project's hostname records are written. */
  dns?: DnsDescriptor;
  /** The needs this project waives rather than have a deploy refused over. */
  allowDegraded?: Need[];
  apps?: AppConfig[];
  domains?: ProjectDomainConfig;
}

export function defineConfig(config: OcelConfig): OcelConfig {
  return config;
}
