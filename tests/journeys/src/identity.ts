import { userInfo } from "node:os";

export function runIdentity(
  env: NodeJS.ProcessEnv,
  username: string,
): string {
  const ci = env.GITHUB_RUN_ID;
  if (ci) {
    return ci;
  }
  return `local-${username}`;
}

export function currentRunIdentity(): string {
  return runIdentity(process.env, userInfo().username);
}

export function projectSlug(example: string, run: string | undefined): string {
  return run ? `j-${run}-${example}` : example;
}

export function appHostname(
  app: string,
  slug: string,
  zone: string | undefined,
): string | undefined {
  return zone ? `${app}.${slug}.${zone}` : undefined;
}
