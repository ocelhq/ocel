export const environments = ["production", "preview"] as const;

export type Environment = (typeof environments)[number];

export function environmentOf(value: string | null | undefined): Environment {
  return value === "preview" ? "preview" : "production";
}

export function withEnvironment(path: string, environment: Environment): string {
  return environment === "production" ? path : `${path}?env=${environment}`;
}

export const runScopes = ["all", ...environments] as const;

export type RunScope = (typeof runScopes)[number];

export function runScopeOf(value: string | null | undefined): RunScope {
  return runScopes.includes(value as RunScope) ? (value as RunScope) : "all";
}
