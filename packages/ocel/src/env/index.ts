import type { Env } from "./access.js";
import type { EnvDefinitions } from "./definition.js";
import { defineServerEnv, serverAccess } from "./server-env.js";

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

export function defineEnv<const TDefinitions extends EnvDefinitions>(
  definitions: TDefinitions,
): Env<TDefinitions> {
  return defineServerEnv(definitions, serverAccess);
}
