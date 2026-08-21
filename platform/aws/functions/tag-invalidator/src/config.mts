export interface Config {
  table: string;
  bootstrapClass: string;
}

function required(env: NodeJS.ProcessEnv, name: string): string {
  const value = env[name];
  if (value === undefined || value === "") {
    throw new Error(`${name} is not set; this function cannot invalidate without it`);
  }
  return value;
}

export function config(env: NodeJS.ProcessEnv): Config {
  return {
    table: required(env, "OCEL_STATE_TABLE"),
    bootstrapClass: required(env, "OCEL_INFRA_CLASS"),
  };
}
