import type { ExecOptions, InfisicalOptions } from "./generated/config.js";

export type { ExecOptions, InfisicalOptions };

/**
 * Declares Infisical as where a tier's values are read from. A deployed tier
 * signs in as the machine identity `auth` names; `ocel dev` signs in as you,
 * from `INFISICAL_TOKEN` or the `infisical` CLI you are logged in to.
 */
export function infisical(options: InfisicalOptions): { infisical: InfisicalOptions } {
  return { infisical: options };
}

/**
 * Declares a command whose output is a tier's values. It runs on your machine,
 * once per variables folder with `{folder}` replaced, and is read at deploy
 * and by `ocel dev`, never on a schedule.
 */
export function exec(options: ExecOptions): { exec: ExecOptions } {
  return { exec: options };
}
