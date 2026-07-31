import type { StandardSchemaV1 } from "@standard-schema/spec";
import { defer } from "../utils/defer.js";
import { declareEnv } from "./declare.js";
import {
  complaint,
  isLive,
  parse,
  validateDefinitions,
  type Definitions,
  type VariableDefinition,
} from "./definition.js";
import { liveGeneration, NO_GENERATION, readLive } from "./live.js";
import { assertInScope, callSite, inScope } from "./scope.js";

export {
  EnvDefinitionError,
  type Definitions,
  type VariableClass,
  type VariableDefinition,
} from "./definition.js";
export { EnvScopeError } from "./scope.js";

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
  const source = callSite();
  validateDefinitions(definitions, source);

  if (process.env.OCEL_PHASE === "discovery") {
    defer(declareEnv(definitions, source));
  }

  const resolved = new Map<string, { generation: number; value: unknown }>();
  const env = new Proxy({} as Env<TDefinitions>, {
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

// FIXED is the generation of every class the process cannot be handed a new
// value for. Such a value is settled before the process starts, so its first
// resolution is its only one — which is what keeps a read a property access.
const FIXED = -1;

// generationOf is what a resolved value is memoised against. A live value is
// memoised against the push it came from rather than against the fact that it
// was read, because a memo that outlived a push would serve the value the
// membrane already replaced until the sandbox died — for as long as the
// sandbox lived, which no staleness bound could cover.
//
// The honest limit: this binds the bound to a read *through this object*.
// `const key = env.SECRET` at module scope copies the string out, and nothing
// can revisit that copy afterwards; a rotation is observed by code that reads
// `env.SECRET` where it uses it.
function generationOf(definition: VariableDefinition): number {
  return isLive(definition) ? liveGeneration() : FIXED;
}

// validateLiveValues runs every live key's schema at the declaration. A live
// value is fetched after this process started, so it is the only class whose
// value can have drifted from its schema since the deploy that shipped the code
// reading it — every other class was checked against the value it would carry
// before the deploy proceeded. Reading it here is what turns that drift into a
// failed init, before the application serves anything, instead of a throw in
// whichever request happened to read it first.
//
// It checks only what a push actually delivered. With no push there is nothing
// that could have drifted, and a key scoped to another app's folder is left
// alone because refusing that read is precisely what its scope is for.
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

  const raw = read(key, definition);
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

// BAKED_PREFIX is the namespaced name the membrane injects an encrypted-baked
// value under, after opening the ciphertext that shipped inside the bundle. It
// is namespaced precisely because such a value must not be readable from the
// environment under the name the user chose.
const BAKED_PREFIX = "OCEL_VAR_";

// read is the one place a value's delivery is known. Every class resolves to
// a plain property read above it, so how a class arrives changes only here.
//
// A live value is asked for by its bare key and nothing else — no folder, no
// coordinate, no store shape. That is what keeps the runtime folder-blind: the
// deploy pinned the coordinate, the membrane spent it, and what came back is a
// flat map under the names the application chose.
//
// A live key falls through to the environment when no push holds it, which is
// how `ocel dev` delivers one: dev has no membrane, so it delivers every class
// the way it delivers the rest, and the call site stays identical. In a deploy
// nothing ever sets those names for a live key, so the fall-through lands on
// the same "no value" failure a missing push would give anyway.
//
// The namespaced name is consulted before the bare one: a value delivered
// there was delivered there deliberately, and nothing sharing its bare name may
// stand in for it.
function read(key: string, definition: VariableDefinition): string | undefined {
  if (isLive(definition)) {
    const pushed = readLive(key);
    if (pushed !== undefined) return pushed;
  }
  return process.env[BAKED_PREFIX + key] ?? process.env[key];
}

function unset(key: string): EnvValueError {
  return new EnvValueError(
    `'${key}' has no value. Set one with \`ocel env set ${key} <VALUE>\`.`,
  );
}
