import path from "node:path";

export const DEFAULT_REGION = "us-east-1";

export const PROFILE_VARS = ["AWS_PROFILE", "AWS_DEFAULT_PROFILE"];

export type AccountFiles = { config: string; credentials: string };

export function accountFiles(dir: string): AccountFiles {
  return { config: path.join(dir, "config"), credentials: path.join(dir, "credentials") };
}

export function pinnedEnv(env: NodeJS.ProcessEnv, dir: string): NodeJS.ProcessEnv {
  const { config, credentials } = accountFiles(dir);
  const pinned: NodeJS.ProcessEnv = { ...env };
  for (const name of PROFILE_VARS) {
    delete pinned[name];
  }
  pinned.AWS_CONFIG_FILE = config;
  pinned.AWS_SHARED_CREDENTIALS_FILE = credentials;
  pinned.AWS_REGION ??= DEFAULT_REGION;
  pinned.AWS_DEFAULT_REGION ??= pinned.AWS_REGION;
  return pinned;
}
