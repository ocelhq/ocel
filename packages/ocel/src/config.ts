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

/** The framework an app is served with. */
export type Framework = "next" | "express" | "fastify" | "hono";

/**
 * How a container app's image is built. Left off, the image is built from the
 * workspace root the app is a member of — its own directory when it belongs to
 * no workspace — with a `Dockerfile` sitting in the app if there is one, and
 * otherwise with no configuration at all.
 */
export interface BuildConfig {
  /**
   * The Dockerfile to build from. A relative path resolves against the app's
   * directory and is free to point outside it; an absolute path is taken as
   * written, which ties the config to the machine it is read on. Naming one
   * builds from it whether or not the app has a `Dockerfile` of its own.
   */
  dockerfile?: string;
  /**
   * The directory the image is built from, relative to the project. It must
   * hold the app. Left off, it is the workspace root the app is a member of,
   * which is what an installer needs to resolve a `workspace:` dependency.
   */
  context?: string;
  /**
   * The command that builds the app inside the image, run from the build
   * context. Left off, the app's own `build` script runs, addressed through
   * the workspace's package manager. Name one to build with turbo, nx or
   * anything else that drives the build from the root.
   */
  command?: string;
}

/**
 * How a container app is checked before it is served. A release is held
 * until the new process answers, and rolled back rather than served if it
 * never does.
 */
export interface HealthConfig {
  /**
   * The path the check requests, off the app's own root. Any 2xx answer means
   * up. Left off, the check requests `/`.
   */
  path?: string;
}

interface AppFields {
  name: string;
  path: string;
  entrypoint?: string;
  domains?: AppDomainConfig;
  folder?: string;
}

/**
 * An app in a project. A serverless app is packed per route and declares the
 * framework that is packed; a container app is one image serving everything,
 * built from the app's directory, so it declares a framework only if it wants
 * to. An app that leaves `compute` to its provider may land on either, so it
 * declares one.
 */
export type AppConfig =
  | (AppFields & { compute?: "serverless"; framework: Framework })
  | (AppFields & {
      compute: "container";
      framework?: Framework;
      build?: BuildConfig;
      health?: HealthConfig;
    });

/**
 * The registry a project's container images are pushed to, and pulled from.
 * Naming one overrides whatever registry the provider offers natively; leaving
 * it off uses the provider's own, and a deploy that needs a registry with
 * neither in reach is refused rather than landing somewhere public.
 */
export interface RegistryConfig {
  /**
   * The registry host, and the namespace images sit under where the registry
   * wants one — `ghcr.io/acme`. A registry that takes repositories at its root
   * takes the host alone. No scheme, and no credentials.
   */
  server: string;
  /** The username the push authenticates as, where the registry wants one. */
  username?: string;
  /**
   * The name of the environment variable holding the password or token — not
   * the secret itself, and written the way an environment variable is, in
   * upper case. It is read on the machine running the deploy, at the moment of
   * the push, so it never reaches a config file or the provider.
   */
  password: string;
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
  /** Where this project's container images are pushed. */
  registry?: RegistryConfig;
}

export function defineConfig(config: OcelConfig): OcelConfig {
  return config;
}
