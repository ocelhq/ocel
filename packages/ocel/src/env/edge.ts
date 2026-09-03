import type { StandardSchemaV1 } from "@standard-schema/spec";
import {
  isLive,
  validateDefinitions,
  type Definitions,
  type VariableDefinition,
} from "./definition.js";
import { EnvEdgeError } from "./errors.js";
import { callSite } from "./callsite.js";
import { assertInScope } from "./scope.js";
import { coerce, readDelivered, undeclared } from "./value.js";

export {
  type Definitions,
  type VariableClass,
  type VariableDefinition,
} from "./definition.js";
export { EnvClientError } from "./client.js";
export { EnvDefinitionError, EnvEdgeError, EnvValueError } from "./errors.js";
export { EnvScopeError } from "./scope.js";

export type Env<TDefinitions extends Definitions> = {
  readonly [K in keyof TDefinitions]: TDefinitions[K]["schema"] extends StandardSchemaV1
    ? StandardSchemaV1.InferOutput<TDefinitions[K]["schema"]>
    : string;
};

const ENTRY_GLOBAL = "__OCEL_EDGE_ENTRY";

export function defineEnv<const TDefinitions extends Definitions>(
  definitions: TDefinitions,
): Env<TDefinitions> {
  validateDefinitions(definitions, callSite());

  const resolved = new Map<string, unknown>();
  return new Proxy({} as Env<TDefinitions>, {
    get(_target, property) {
      if (typeof property === "symbol") return undefined;

      const key = property;
      const definition = definitions[key];
      if (!definition) throw undeclared(key);
      if (resolved.has(key)) return resolved.get(key);

      const value = resolve(key, definition);
      resolved.set(key, value);
      return value;
    },
  });
}

function resolve(key: string, definition: VariableDefinition): unknown {
  assertInScope(key, definition.folders ?? []);
  if (isLive(definition)) throw notLive(key, definition);
  return coerce(key, definition, readDelivered(key));
}

function notLive(key: string, definition: VariableDefinition): EnvEdgeError {
  const entry = (globalThis as Record<string, unknown>)[ENTRY_GLOBAL];
  const where =
    typeof entry === "string" && entry !== ""
      ? `edge entry '${entry}'`
      : "an edge entry";
  return new EnvEdgeError(
    `'${key}' is class '${definition.class}' and cannot be read from ${where}: a '${definition.class}' value is read live on every request, and the edge tier has no live channel to read it over. Move this entry to the nodejs runtime, or declare '${key}' as 'sensitive'.`,
  );
}
