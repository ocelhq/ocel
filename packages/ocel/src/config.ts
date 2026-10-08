export { type BuildEnv, BuildEnvError, buildEnv } from "./build-env.js";
export type {
  AppConfig,
  AppDomainConfig,
  AwsDNSDescriptor,
  AwsEdgeDescriptor,
  AwsProviderOptions,
  ComputeDescriptor,
  ContainerCompute,
  DevEnvSourceDescriptor,
  DiscoveryConfig,
  EnvSourceConfig,
  EnvSourceDescriptor,
  ExecOptions,
  GcpDNSDescriptor,
  GcpEdgeDescriptor,
  GcpProviderOptions,
  HealthConfig,
  ImageConfig,
  InfisicalOptions,
  InstancesConfig,
  OcelConfig,
  ProjectDomainConfig,
  ProviderDescriptor,
  RegistryConfig,
  ServerlessCompute,
  VpsDNSDescriptor,
  VpsEdgeDescriptor,
  VpsProviderOptions,
  VpsTarget,
} from "./generated/config.js";

import type { ComputeDescriptor, OcelConfig, ServerlessCompute } from "./generated/config.js";

/**
 * Something an app needs of wherever it is served. A need listed in
 * `allowDegraded` is waived, and the deploy proceeds without it; an unwaived
 * need the deploy cannot meet refuses the deploy instead of silently serving
 * less than the app asks for.
 */
export type Need = NonNullable<OcelConfig["allowDegraded"]>[number];

/** What an app runs on, named alone. */
export type Compute = Extract<ComputeDescriptor, string>;

/** What a serverless app is built with. */
export type Framework = NonNullable<ServerlessCompute["framework"]>;

/**
 * Declares a project. The object it takes is the same document `ocel.json`
 * contains, so a TypeScript config is an authoring surface over data and nothing
 * more: nothing in it is evaluated at deploy time.
 */
export function defineConfig(config: OcelConfig): OcelConfig {
  return config;
}
