import type { Env } from "./access.js";
import type { EnvDefinitions } from "./definition.js";
import { defineEdgeEnv, edgeAccess } from "./edge-env.js";
import {
  type PublicPlain,
  refusePublicConfidential,
  withInlinedPublicValues,
} from "./next-public.js";

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
export { type Deployment, deployment } from "./deployment.js";
export { EnvClientError, EnvDefinitionError, EnvEdgeError, EnvValueError } from "./errors.js";
export { EnvScopeError } from "./scope.js";

/**
 * Declares the environment of a Next app on the edge runtime. A variable starting with
 * `NEXT_PUBLIC_` must be class `plain` and is read from the values Next inlined at build
 * time; every other variable is read from the edge's environment.
 */
export function defineEnv<const TDefinitions extends EnvDefinitions>(
  definitions: TDefinitions & PublicPlain<TDefinitions>,
): Env<TDefinitions> {
  refusePublicConfidential(definitions);
  return defineEdgeEnv(definitions, withInlinedPublicValues(edgeAccess));
}
