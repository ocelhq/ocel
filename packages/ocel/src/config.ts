// ProviderDescriptor identifies the npm package a provider function (e.g.
// awsProvider) is exported from and the opaque options the user passed it.
// The CLI uses `package` to locate the provider's binary and forwards
// `options` to the provider unexamined.
export interface ProviderDescriptor {
  package: string;
  options: unknown;
}

// DomainConfig maps an environment class to the custom hostname(s) served for
// it. It is the same shape at the project level and per app; an app's entry
// wins for that app. Production may name several hostnames (an apex plus its
// aliases, or a "*." wildcard for a multitenant app), each attached as a
// Cloudflare worker route; preview names a single wildcard the per-pointer
// preview subdomains live under.
export interface DomainConfig {
  production?: string | string[];
  preview?: string;
}

// AppConfig declares an application Ocel builds and deploys. `framework` is
// restricted to the frameworks Ocel supports this iteration; `entrypoint` is
// an optional override relative to `path`.
export interface AppConfig {
  name: string;
  path: string;
  framework: "next" | "express" | "fastify";
  entrypoint?: string;
  domains?: DomainConfig;
  // folder is the variable folder this app's values come from — an absolute
  // path like "/checkout", and the reason two apps in one project can require
  // the same key name and get different values. Omit it to read the project
  // root. Binding is a deployment concern, which is why it is declared here
  // and not beside the variables themselves.
  folder?: string;
}

export interface OcelConfig {
  // slug is the project's identity: a DNS-label string
  // (^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$) that everything project-scoped
  // derives from — Pulumi stack names, SSM root-stack state keys, and the
  // project's instance in the shared deployments-store worker. It is treated as
  // immutable; changing it forks a new project with fresh infrastructure. ocel
  // init pre-fills a sanitized directory-name default.
  slug: string;
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
