export interface ProviderDescriptor {
  package: string;
  options: unknown;
}

export interface AppDomainConfig {
  production?: string | string[];
}

export interface ProjectDomainConfig extends AppDomainConfig {
  preview?: string;
}

export interface AppConfig {
  name: string;
  path: string;
  framework: "next" | "express" | "fastify";
  entrypoint?: string;
  domains?: AppDomainConfig;
  folder?: string;
}

export interface OcelConfig {
  slug: string;
  discovery?: {
    paths?: string[];
  };
  provider?: ProviderDescriptor;
  apps?: AppConfig[];
  domains?: ProjectDomainConfig;
}

export function defineConfig(config: OcelConfig): OcelConfig {
  return config;
}
