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

// ENTRY_GLOBAL is where the edge shim records which entry a request is running
// (packages/next-runtime's renderEdgeShim, set before the entry's chunks
// evaluate). It is a global rather than an argument because a variable is read
// as a plain property and has nowhere to be handed one.
const ENTRY_GLOBAL = "__OCEL_EDGE_ENTRY";

// The edge build of `ocel/env`, selected by the `edge-light`, `workerd` and
// `worker` export conditions. It is a whole module rather than a branch because
// the node build evaluates `@connectrpc/connect-node` at module scope, which
// fails an edge compile on `node:http2` before any branch could run.
//
// Declaring stays harmless: a definitions module is normally shared by node and
// edge entries, so throwing at `defineEnv` would break an app whose edge routes
// merely import the module they never read from. Only the read is refused.
export function defineEnv<const TDefinitions extends Definitions>(
  definitions: TDefinitions,
): Env<TDefinitions> {
  validateDefinitions(definitions, callSite());

  return new Proxy({} as Env<TDefinitions>, {
    get(_target, property) {
      // Symbols are how the runtime inspects an object; they are never a
      // variable, and answering them keeps the object loggable.
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
