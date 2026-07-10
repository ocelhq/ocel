// ProviderDescriptor identifies the npm package a provider function (e.g.
// awsProvider) is exported from and the opaque options the user passed it.
// The CLI uses `package` to locate the provider's binary and forwards
// `options` to the provider unexamined.
export interface ProviderDescriptor {
  package: string;
  options: unknown;
}

export interface OcelConfig {
  projectId: string;
  discovery?: {
    paths?: string[];
  };
  provider?: ProviderDescriptor;
}

export function defineConfig(config: OcelConfig): OcelConfig {
  return config;
}
