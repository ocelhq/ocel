import type { StandardSchemaV1 } from "@standard-schema/spec";
import { defer } from "../utils/defer.js";
import { declareEnv } from "./declare.js";
import {
  isLive,
  validateDefinitions,
  type Definitions,
  type VariableDefinition,
} from "./definition.js";
import { EnvValueError } from "./errors.js";
import { liveGeneration, NO_GENERATION, readLive } from "./live.js";
import { assertInScope, callSite, inScope } from "./scope.js";
import { coerce, readDelivered, undeclared } from "./value.js";

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

export function defineEnv<const TDefinitions extends Definitions>(
  definitions: TDefinitions,
): Env<TDefinitions> {
  const source = callSite();
  validateDefinitions(definitions, source);

  if (process.env.OCEL_PHASE === "discovery") {
    defer(declareEnv(definitions, source));
  }

  const resolved = new Map<string, { generation: number; value: unknown }>();
  const env = new Proxy({} as Env<TDefinitions>, {
    get(_target, property) {
      if (typeof property === "symbol") return undefined;

      const key = property;
      const definition = definitions[key];
      if (!definition) throw undeclared(key);
      const generation = generationOf(definition);
      const memo = resolved.get(key);
      if (memo?.generation === generation) return memo.value;

      const value = resolve(key, definition);
      resolved.set(key, { generation, value });
      return value;
    },
  });

  validateLiveValues(definitions, env);
  return env;
}

const FIXED = -1;

function generationOf(definition: VariableDefinition): number {
  return isLive(definition) ? liveGeneration() : FIXED;
}

function validateLiveValues<TDefinitions extends Definitions>(
  definitions: TDefinitions,
  env: Env<TDefinitions>,
): void {
  if (liveGeneration() === NO_GENERATION) return;
  if (process.env.OCEL_PHASE === "discovery") return;

  for (const [key, definition] of Object.entries(definitions)) {
    if (!isLive(definition)) continue;
    if (!inScope(definition.folders ?? [])) continue;
    void env[key as keyof TDefinitions];
  }
}

function resolve(key: string, definition: VariableDefinition): unknown {
  if (process.env.OCEL_PHASE === "discovery") {
    throw new EnvValueError(
      `'${key}' cannot be read during discovery: values are resolved after the requirements are declared.`,
    );
  }

  assertInScope(key, definition.folders ?? []);

  return coerce(key, definition, read(key, definition));
}

function read(key: string, definition: VariableDefinition): string | undefined {
  if (isLive(definition)) {
    const pushed = readLive(key);
    if (pushed !== undefined) return pushed;
  }
  return readDelivered(key);
}
