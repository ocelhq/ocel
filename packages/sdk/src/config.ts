// ProviderDescriptor identifies the npm package a provider function (e.g.
// awsProvider) is exported from and the opaque options the user passed it.
// The CLI uses `package` to locate the provider's binary and forwards
// `options` to the provider unexamined.
export interface ProviderDescriptor {
  package: string;
  options: unknown;
}

// AppConfig declares an application Ocel builds and deploys. `framework` is
// restricted to the frameworks Ocel supports this iteration; `entrypoint` is
// an optional override relative to `path`.
export interface AppConfig {
  name: string;
  path: string;
  framework: "express" | "fastify";
  entrypoint?: string;
}

export interface OcelConfig {
  projectId: string;
  discovery?: {
    paths?: string[];
  };
  provider?: ProviderDescriptor;
  apps?: AppConfig[];
  domains?: {
    production?: string;
  };
}

export function defineConfig(config: OcelConfig): OcelConfig {
  return config;
}
