import { callSiteFile } from "../declaration/callsite.js";
import { type Access, type Env, envAccessor, FIXED } from "./access.js";
import {
  type EnvDefinitions,
  isLive,
  type VariableDefinition,
  validateDefinitions,
} from "./definition.js";
import { EnvEdgeError } from "./errors.js";
import { assertInScope } from "./scope.js";
import { coerce, readDelivered } from "./value.js";

const ENTRY_GLOBAL = "__OCEL_EDGE_ENTRY";

export const edgeAccess: Access = { resolve, delivered, generationOf: () => FIXED };

export function defineEdgeEnv<const TDefinitions extends EnvDefinitions>(
  definitions: TDefinitions,
  access: Access,
): Env<TDefinitions> {
  validateDefinitions(definitions, callSiteFile());

  return envAccessor(definitions, access);
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
