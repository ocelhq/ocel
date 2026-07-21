// ProviderDescriptor identifies the npm package a provider function (e.g.
// awsProvider) is exported from and the opaque options the user passed it.
// The CLI uses `package` to locate the provider's binary and forwards
// `options` to the provider unexamined.
export interface ProviderDescriptor {
  package: string;
  options: unknown;
}

// DomainConfig maps an environment class to the custom hostname served for it.
// It is the same shape at the project level and per app; an app's entry wins
// for that app.
export interface DomainConfig {
  production?: string;
}

// AppConfig declares an application Ocel builds and deploys. `framework` is
// restricted to the frameworks Ocel supports this iteration; `entrypoint` is
// an optional override relative to `path`.
export interface AppConfig {
  name: string;
  path: string;
  framework: "express" | "fastify";
  entrypoint?: string;
  domains?: DomainConfig;
}

export interface OcelConfig {
  // slug is the project's stable, human-authored deployment identity: a
  // DNS-label string (^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$) that keys the
  // project's own instance in the shared deployments-store worker. It is
  // distinct from projectId (the ocel dev-cloud link, on its way out) and is
  // treated as immutable — changing it forks a new project with fresh
  // deployment history. ocel init pre-fills a sanitized directory-name default.
  slug: string;
  projectId?: string;
  discovery?: {
    paths?: string[];
  };
  provider?: ProviderDescriptor;
  apps?: AppConfig[];
  domains?: DomainConfig;
}

export function defineConfig(config: OcelConfig): OcelConfig {
  return config;
}
