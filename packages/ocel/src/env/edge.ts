import { callSiteFile } from "../utils/callsite.js";
import { type Env, envAccessor, FIXED } from "./access.js";
import {
  type EnvDefinitions,
  isLive,
  type VariableDefinition,
  validateDefinitions,
} from "./definition.js";
import { EnvEdgeError } from "./errors.js";
import { assertInScope } from "./scope.js";
import { coerce, readDelivered } from "./value.js";

export type { Env } from "./access.js";
export { EnvClientError } from "./client.js";
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

const ENTRY_GLOBAL = "__OCEL_EDGE_ENTRY";

export function defineEnv<const TDefinitions extends EnvDefinitions>(
  definitions: TDefinitions,
): Env<TDefinitions> {
  validateDefinitions(definitions, callSiteFile());

  return envAccessor(definitions, { resolve, delivered, generationOf: () => FIXED });
}

function delivered(key: string): boolean {
  return readDelivered(key) !== undefined;
}

function resolve(key: string, definition: VariableDefinition): unknown {
  assertInScope(key, definition.folders ?? []);
  if (isLive(definition)) throw notLive(key, definition);
  return coerce(key, definition, readDelivered(key));
}

function notLive(key: string, definition: VariableDefinition): EnvEdgeError {
  const entry = (globalThis as Record<string, unknown>)[ENTRY_GLOBAL];
  const where =
    typeof entry === "string" && entry !== "" ? `edge entry '${entry}'` : "an edge entry";
  return new EnvEdgeError(
    `'${key}' is class '${definition.class}' and cannot be read from ${where}: a '${definition.class}' value is read live on every request, and the edge tier has no live channel to read it over. Move this entry to the nodejs runtime, or declare '${key}' as 'sensitive'.`,
  );
}
