import type { StandardSchemaV1 } from "@standard-schema/spec";
import { defer } from "../utils/defer.js";
import { declareEnv } from "./declare.js";
import {
  parse,
  validateDefinitions,
  type Definitions,
  type VariableDefinition,
} from "./definition.js";

export {
  EnvDefinitionError,
  type Definitions,
  type VariableClass,
  type VariableDefinition,
} from "./definition.js";

// EnvValueError is a variable that cannot be read: nothing set it, or what is
// set does not satisfy its schema. It names the key and the command that
// fixes it, because that is the whole remedy.
export class EnvValueError extends Error {
  override name = "EnvValueError";
}

export type Env<TDefinitions extends Definitions> = {
  readonly [K in keyof TDefinitions]: TDefinitions[K]["schema"] extends StandardSchemaV1
    ? StandardSchemaV1.InferOutput<TDefinitions[K]["schema"]>
    : string;
};

// defineEnv declares the variables this project cannot run without and hands
// back the object its code reads them from. Declaring is what makes a deploy
// refuse before it builds; reading is a plain synchronous property access for
// every class, so a key's class can change without touching a call site.
export function defineEnv<const TDefinitions extends Definitions>(
  definitions: TDefinitions,
): Env<TDefinitions> {
  validateDefinitions(definitions);

  // intentionally defined repeatedly like this for dead code elimination when in prod
  if (process.env.OCEL_PHASE === "discovery") {
    defer(declareEnv(definitions));
  }

  const resolved = new Map<string, unknown>();
  return new Proxy({} as Env<TDefinitions>, {
    get(_target, property) {
      // Symbols are how the runtime inspects an object (console.log, string
      // coercion); they are never a variable, and answering them keeps the
      // object printable.
      if (typeof property === "symbol") return undefined;

      const key = property;
      const definition = definitions[key];
      if (!definition) {
        throw new EnvValueError(
          `'${key}' is not a declared variable. Add it to a defineEnv call.`,
        );
      }
      if (!resolved.has(key)) {
        resolved.set(key, resolve(key, definition));
      }
      return resolved.get(key);
    },
  });
}

function resolve(key: string, definition: VariableDefinition): unknown {
  if (process.env.OCEL_PHASE === "discovery") {
    throw new EnvValueError(
      `'${key}' cannot be read during discovery: values are resolved after the requirements are declared.`,
    );
  }

  const raw = read(key);
  if (!definition.schema) {
    if (raw === undefined) throw unset(key);
    return raw;
  }

  const result = parse(definition.schema, raw);
  if (result.ok) return result.value;
  if (raw === undefined) throw unset(key);
  throw new EnvValueError(
    `'${key}' is set but does not satisfy its schema: ${result.message}. Fix it with \`ocel env set ${key} <VALUE>\`.`,
  );
}

// read is the one place a value's delivery is known. Every class resolves to
// a plain property read above it, so how a class arrives changes only here.
function read(key: string): string | undefined {
  return process.env[key];
}

function unset(key: string): EnvValueError {
  return new EnvValueError(
    `'${key}' has no value. Set one with \`ocel env set ${key} <VALUE>\`.`,
  );
}
