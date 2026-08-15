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
  apps?: AppConfig[];
  domains?: ProjectDomainConfig;
}

export function defineConfig(config: OcelConfig): OcelConfig {
  return config;
}
