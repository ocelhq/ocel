import { callSiteFile } from "../declaration/callsite.js";
import { defer } from "../declaration/defer.js";
import { type Access, type Env, envAccessor, FIXED, UNCACHED } from "./access.js";
import { declareEnv } from "./declare.js";
import {
  type EnvDefinitions,
  type FlatDefinitions,
  isLive,
  type VariableDefinition,
  validateDefinitions,
} from "./definition.js";
import { EnvValueError } from "./errors.js";
import { readLiveFile } from "./file.js";
import { liveGeneration, NO_GENERATION, readLive } from "./live.js";
import { assertInScope, inScope } from "./scope.js";
import { coerce, readDelivered } from "./value.js";

export const serverAccess: Access = { resolve, delivered, generationOf };

export function defineServerEnv<const TDefinitions extends EnvDefinitions>(
  definitions: TDefinitions,
  access: Access,
): Env<TDefinitions> {
  const source = callSiteFile();
  const flat = validateDefinitions(definitions, source);

  if (process.env.OCEL_PHASE === "discovery") {
    defer(declareEnv(flat, source));
  }

  const env = envAccessor(definitions, access);

  validateLiveValues(flat, env);
  return env;
}

function delivered(key: string, definition: VariableDefinition): boolean {
  return read(key, definition) !== undefined;
}

function generationOf(definition: VariableDefinition): number {
  if (!isLive(definition)) return FIXED;
  const generation = liveGeneration();
  return generation === NO_GENERATION ? UNCACHED : generation;
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
  return readDelivered(key) ?? readLiveFile(key);
}
