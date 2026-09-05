import { userInfo } from "node:os";

export const HARNESS_PREFIX = "j-";

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

export function slugPart(cell: string): string {
  return cell.replace(/\//g, "-");
}

export function projectSlug(cell: string, run: string): string {
  return `${HARNESS_PREFIX}${run}-${slugPart(cell)}`;
}

const LONGEST_LABEL = 63;

export function appHostname(
  app: string,
  slug: string,
  zone: string | undefined,
): string | undefined {
  if (!zone) {
    return undefined;
  }
  const label = `${app}-${slug}`;
  if (label.length > LONGEST_LABEL) {
    throw new Error(
      `${label} is ${label.length} characters, and a dns label holds ${LONGEST_LABEL}. ` +
        `Shorten the app name or the cell name behind ${slug}.`,
    );
  }
  return `${label}.${zone}`;
}
