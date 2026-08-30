export { buildEnv, BuildEnvError, type BuildEnv } from "./build-env.js";

export interface ProviderDescriptor {
  package: string;
  options: unknown;
}

declare const edgeDescriptor: unique symbol;

/**
 * The edge a project's hostnames are served from. An edge is never written out
 * by hand: call a factory that names one — `cloudflare()` from `ocel/edge`, or
 * one your provider ships, such as `cloudfront()` and `apiGateway()` from
 * `@ocel/provider-aws/edge`.
 */
export interface EdgeDescriptor {
  readonly [edgeDescriptor]: true;
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

/**
 * What an app runs on. Left off, an app runs on whatever its provider names
 * first, so a project that has only ever wanted its provider's usual shape
 * writes nothing. A compute the provider does not run refuses the deploy by
 * name rather than quietly landing something else.
 */
export type Compute = "serverless" | "container";

export interface AppConfig {
  name: string;
  path: string;
  framework: "next" | "express" | "fastify" | "hono";
  compute?: Compute;
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
   * The edge in front of the origin. Omit it and the provider fronts the
   * deployment with its own default edge; name one to choose instead.
   */
  edge?: EdgeDescriptor;
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
