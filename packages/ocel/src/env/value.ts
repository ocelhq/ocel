import { complaint, type VariableDefinition } from "./definition.js";
import { parse } from "./standard.js";
import { EnvValueError } from "./errors.js";

const BAKED_PREFIX = "OCEL_VAR_";

export function readDelivered(key: string): string | undefined {
  return process.env[BAKED_PREFIX + key] ?? process.env[key];
}

export function coerce(
  key: string,
  definition: VariableDefinition,
  raw: string | undefined,
): unknown {
  if (!definition.schema) {
    if (raw === undefined) throw unset(key);
    return raw;
  }

  const result = parse(definition.schema, raw);
  if (result.ok) return result.value;
  if (raw === undefined) throw unset(key);
  throw new EnvValueError(
    `'${key}' is set but does not satisfy its schema: ${complaint(definition, result.message)}. Fix it with \`ocel env set ${key} <VALUE>\`.`,
  );
}

function unset(key: string): EnvValueError {
  return new EnvValueError(
    `'${key}' has no value. Set one with \`ocel env set ${key} <VALUE>\`.`,
  );
}

export function undeclared(key: string): EnvValueError {
  return new EnvValueError(
    `'${key}' is not a declared variable. Add it to a defineEnv call.`,
  );
}
