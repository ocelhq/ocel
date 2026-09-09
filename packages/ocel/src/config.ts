export { type BuildEnv, BuildEnvError, buildEnv } from "./build-env.js";
export type {
  AppConfig,
  AppDomainConfig,
  AwsProviderOptions,
  BuildConfig,
  DiscoveryConfig,
  DnsDescriptor,
  EdgeDescriptor,
  GcpProviderOptions,
  HealthConfig,
  OcelConfig,
  ProjectDomainConfig,
  ProviderDescriptor,
  RegistryConfig,
  RuntimeObject,
  VpsProviderOptions,
  VpsTarget,
} from "./generated/config.js";

import type { OcelConfig } from "./generated/config.js";

/**
 * Something an app needs of wherever it is served. A need listed in
 * `allowDegraded` is waived, and the deploy proceeds without it; an unwaived
 * need the deploy cannot meet refuses the deploy instead of silently serving
 * less than the app asks for.
 */
export type Need = NonNullable<OcelConfig["allowDegraded"]>[number];

/** What an app runs on. */
export type Compute = NonNullable<AppComputeOf<OcelConfig>>;

type AppComputeOf<T> = T extends { apps?: (infer A)[] }
  ? A extends { compute?: infer C }
    ? C
    : never
  : never;

/** The runtime a serverless app's functions run on. */
export type Runtime = NonNullable<AppRuntimeOf<OcelConfig>>;

type AppRuntimeOf<T> = T extends { apps?: (infer A)[] }
  ? A extends { runtime?: infer R }
    ? R
    : never
  : never;

/**
 * Declares a project. The object it takes is the same document `ocel.json`
 * holds, so a TypeScript config is an authoring surface over data and nothing
 * more: nothing in it is evaluated at deploy time.
 */
export function defineConfig(config: OcelConfig): OcelConfig {
  return config;
}
