// ProviderDescriptor identifies the npm package a provider function (e.g.
// awsProvider) is exported from and the opaque options the user passed it.
// The CLI uses `package` to locate the provider's binary and forwards
// `options` to the provider unexamined.
export interface ProviderDescriptor {
  package: string;
  options: unknown;
}

// AppDomainConfig is the production hostname(s) one app is served on: an apex
// plus its aliases, say, or a "*." wildcard for a multitenant app, each attached
// as its own worker route. An app's entry wins for that app; the project-level
// entry covers the apps that declare none.
export interface AppDomainConfig {
  production?: string | string[];
}

// ProjectDomainConfig adds the preview wildcard, which is project-level only: a
// preview domain is claimed by the whole project, whose one preview entrypoint
// worker serves every app under it, so an app cannot declare one of its own.
export interface ProjectDomainConfig extends AppDomainConfig {
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
  domains?: AppDomainConfig;
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
  domains?: ProjectDomainConfig;
}

export function defineConfig(config: OcelConfig): OcelConfig {
  return config;
}
