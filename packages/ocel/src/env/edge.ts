import type { StandardSchemaV1 } from "@standard-schema/spec";
import { validateDefinitions, type Definitions } from "./definition.js";
import { EnvEdgeError } from "./errors.js";
import { callSite } from "./scope.js";

export {
  EnvDefinitionError,
  type Definitions,
  type VariableClass,
  type VariableDefinition,
} from "./definition.js";
export { EnvClientError } from "./client.js";
export { EnvEdgeError, EnvValueError } from "./errors.js";
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

  return new Proxy({} as Env<TDefinitions>, {
    get(_target, property) {
      if (typeof property === "symbol") return undefined;
      throw unreadable(property);
    },
  });
}

function unreadable(key: string): EnvEdgeError {
  const entry = (globalThis as Record<string, unknown>)[ENTRY_GLOBAL];
  const where =
    typeof entry === "string" && entry !== ""
      ? `edge entry '${entry}'`
      : "an edge entry";
  return new EnvEdgeError(
    `'${key}' cannot be read from ${where}: no variable class is deliverable to the edge runtime — a value reaches a function's environment, a bundle the membrane opens, or the membrane itself, and the edge tier has none of them. Move this entry to the nodejs runtime.`,
  );
}
