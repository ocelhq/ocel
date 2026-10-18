import type { Env } from "./access.js";
import type { EnvDefinitions } from "./definition.js";
import { defineEdgeEnv, edgeAccess } from "./edge-env.js";

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
export { EnvDefinitionError, EnvEdgeError, EnvValueError } from "./errors.js";
export { EnvScopeError } from "./scope.js";

/**
 * Declares the app's environment variables and returns them typed, each read on access
 * from the values delivered to this edge function.
 */
export function defineEnv<const TDefinitions extends EnvDefinitions>(
  definitions: TDefinitions,
): Env<TDefinitions> {
  return defineEdgeEnv(definitions, edgeAccess);
}
