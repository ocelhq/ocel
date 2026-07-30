import type { StandardSchemaV1 } from "@standard-schema/spec";
import { scopeProblem } from "./scope.js";

export type VariableClass = "plain" | "sensitive" | "secret";

export interface VariableDefinition<
  TSchema extends StandardSchemaV1 = StandardSchemaV1,
> {
  // class decides confidentiality and delivery together. Reading never varies
  // with it, so reclassifying a key never edits a call site.
  class: VariableClass;

  // schema is any Standard Schema. It is also the only place a default may
  // come from, so there is exactly one answer to where a value came from.
  schema?: TSchema;

  // client marks a value the browser may read. It forces the plaintext class.
  client?: boolean;

  // folders scopes the variable to the apps bound to exactly these folders,
  // for the one case folders exist to serve: two apps that need the same key
  // name and different values. It is then mandatory in every folder it names
  // and has no root value at all — an unscoped variable lives at the root and
  // a bound app may override it there, and the two modes never mix.
  folders?: readonly string[];
}

export type Definitions = Record<string, VariableDefinition>;

// EnvDefinitionError is a declaration that cannot be honoured — a duplicate
// key, a reserved name, or a combination of options that contradict each
// other. It is thrown where the definition is written, not where a value is
// read.
export class EnvDefinitionError extends Error {
  override name = "EnvDefinitionError";
}

// livingClasses are fetched from the store at runtime rather than carried by
// an artifact, so a default would let a rotation-in-progress silently serve a
// fallback instead of failing.
const LIVE_CLASSES: ReadonlySet<VariableClass> = new Set(["secret"]);

// bareKeyClasses reach the process environment under the key's own name (or,
// for a client-accessible key, under the framework's public prefix), which is
// the only situation where a platform-owned name could be overwritten.
const BARE_KEY_CLASSES: ReadonlySet<VariableClass> = new Set(["plain"]);

const RESERVED_PREFIXES = ["OCEL_", "AWS_", "LAMBDA_", "NEXT_PUBLIC_"];

const KEY_PATTERN = /^[A-Z_][A-Z0-9_]*$/;

// declared is process-global on purpose: definitions may be spread across
// files, and a duplicate key is only observable from above all of them.
const declared = new Set<string>();

export function validateDefinitions(definitions: Definitions): void {
  for (const [key, definition] of Object.entries(definitions)) {
    validateDefinition(key, definition);
    declared.add(key);
  }
}

function validateDefinition(key: string, definition: VariableDefinition): void {
  if (!KEY_PATTERN.test(key)) {
    throw new EnvDefinitionError(
      `'${key}' is not a usable variable name: use upper-case letters, digits and underscores, starting with a letter or underscore.`,
    );
  }
  if (declared.has(key)) {
    throw new EnvDefinitionError(
      `'${key}' is declared twice. A key may be defined by exactly one defineEnv call.`,
    );
  }
  if (definition.client && definition.class !== "plain") {
    throw new EnvDefinitionError(
      `'${key}' is client-accessible and class '${definition.class}'. A value the browser can read cannot be an encrypted class.`,
    );
  }
  if (
    (BARE_KEY_CLASSES.has(definition.class) || definition.client) &&
    RESERVED_PREFIXES.some((prefix) => key.startsWith(prefix))
  ) {
    throw new EnvDefinitionError(
      `'${key}' starts with a reserved prefix (${RESERVED_PREFIXES.join(", ")}). A '${definition.class}' variable is delivered under its own name, so the platform would overwrite it.`,
    );
  }
  if (definition.folders) {
    const problem = scopeProblem(definition.folders);
    if (problem) {
      throw new EnvDefinitionError(`'${key}' has an unusable folder scope: ${problem}`);
    }
  }
  if (LIVE_CLASSES.has(definition.class) && !isRequired(definition)) {
    throw new EnvDefinitionError(
      `'${key}' is class '${definition.class}' and its schema supplies a default. A live value must fail loudly when it is missing rather than fall back.`,
    );
  }
}

// isRequired asks the schema whether it accepts nothing, which is the only
// portable way to learn that a Standard Schema carries a default.
export function isRequired(definition: VariableDefinition): boolean {
  if (!definition.schema) return true;
  return parse(definition.schema, undefined).ok === false;
}

export type ParseResult =
  | { ok: true; value: unknown }
  | { ok: false; message: string };

// parse runs a Standard Schema and refuses an asynchronous one: a variable is
// read as a plain synchronous property, so a schema that cannot answer
// synchronously could never be honoured at the point of use.
export function parse(
  schema: StandardSchemaV1,
  value: unknown,
): ParseResult {
  const result = schema["~standard"].validate(value);
  if (result instanceof Promise) {
    throw new EnvDefinitionError(
      "A variable's schema must validate synchronously: variables are read as plain properties.",
    );
  }
  if (result.issues) {
    return { ok: false, message: describeIssues(result.issues) };
  }
  return { ok: true, value: result.value };
}

function describeIssues(
  issues: ReadonlyArray<StandardSchemaV1.Issue>,
): string {
  return issues
    .map((issue) => {
      const path = (issue.path ?? [])
        .map((segment) =>
          typeof segment === "object" ? String(segment.key) : String(segment),
        )
        .join(".");
      return path ? `${path}: ${issue.message}` : issue.message;
    })
    .join("; ");
}
