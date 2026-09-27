import type { ExecOptions, InfisicalOptions } from "./config.js";

/**
 * Declares Infisical as where a tier's values are read from. Production and
 * preview sign in as the machine identity `auth` names; `ocel dev` signs in as
 * you, from `INFISICAL_TOKEN` or else the `infisical` CLI you are logged in to.
 */
export function infisical(options: InfisicalOptions): { infisical: InfisicalOptions } {
  return { infisical: options };
}

/**
 * Declares a command whose output is a tier's values. It runs on the machine
 * running ocel, once per variables folder with `{folder}` replaced, at each
 * deploy and each `ocel dev`, and never on a schedule.
 */
export function exec(options: ExecOptions): { exec: ExecOptions } {
  return { exec: options };
}
