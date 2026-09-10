import { callSiteFile } from "../utils/callsite.js";
import { defer } from "../utils/defer.js";
import { type Env, envAccessor, FIXED } from "./access.js";
import { declareEnv } from "./declare.js";
import {
  type EnvDefinitions,
  type FlatDefinitions,
  isLive,
  type VariableDefinition,
  validateDefinitions,
} from "./definition.js";
import { EnvValueError } from "./errors.js";
import { liveGeneration, NO_GENERATION, readLive } from "./live.js";
import { sourceOf } from "./schema.js";
import { assertInScope, inScope } from "./scope.js";
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

export function defineEnv<const TDefinitions extends EnvDefinitions>(
  definitions: TDefinitions,
): Env<TDefinitions> {
  const source = callSiteFile();
  const flat = validateDefinitions(definitions, source);

  if (process.env.OCEL_PHASE === "discovery") {
    defer(declareEnv(flat, source, sourceOf(definitions)));
  }

  const env = envAccessor(definitions, { resolve, delivered, generationOf });

  validateLiveValues(flat, env);
  return env;
}

function delivered(key: string, definition: VariableDefinition): boolean {
  return read(key, definition) !== undefined;
}

function generationOf(definition: VariableDefinition): number {
  return isLive(definition) ? liveGeneration() : FIXED;
}

function validateLiveValues(definitions: FlatDefinitions, env: Env<EnvDefinitions>): void {
  if (liveGeneration() === NO_GENERATION) return;
  if (process.env.OCEL_PHASE === "discovery") return;

  for (const [key, definition] of Object.entries(definitions)) {
    if (!isLive(definition)) continue;
    if (!inScope(definition.folders ?? [])) continue;
    void env[definition.group ?? key];
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
