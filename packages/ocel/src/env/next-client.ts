import type { Env } from "./access.js";
import type { EnvDefinitions } from "./definition.js";
import { inlinedEnv, type PublicPlain, refusePublicConfidential } from "./next-public.js";

export type { Env } from "./access.js";
export type {
  Definitions,
  EnvDefinitions,
  GroupDefinition,
  GroupOptions,
  VariableClass,
  VariableDefinition,
} from "./definition.js";
export { group } from "./definition.js";
export { EnvClientError, EnvDefinitionError } from "./errors.js";

/**
 * Declares the environment of a Next app. A variable starting with `NEXT_PUBLIC_` must be
 * class `plain` and is readable in the browser; every other variable is server-only and
 * throws when read there.
 */
export function defineEnv<const TDefinitions extends EnvDefinitions>(
  definitions: TDefinitions & PublicPlain<TDefinitions>,
): Env<TDefinitions> {
  refusePublicConfidential(definitions);
  return inlinedEnv(definitions);
}
