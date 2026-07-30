import type { StandardSchemaV1 } from "@standard-schema/spec";
import { scopeProblem } from "./scope.js";

export type VariableClass = "plain" | "sensitive" | "secret";

// ConfidentialClass is a class whose values are kept from everything that is
// not the running application: encrypted inside the artifact, or never in one.
export type ConfidentialClass = Exclude<VariableClass, "plain">;

interface VariableOptions<TSchema extends StandardSchemaV1 = StandardSchemaV1> {
  // schema is any Standard Schema. It is also the only place a default may
  // come from, so there is exactly one answer to where a value came from.
  schema?: TSchema;

  // folders scopes the variable to the apps bound to exactly these folders,
  // for the one case folders exist to serve: two apps that need the same key
  // name and different values. It is then mandatory in every folder it names
  // and has no root value at all — an unscoped variable lives at the root and
  // a bound app may override it there, and the two modes never mix.
  folders?: readonly string[];
}

// VariableDefinition is split on class so that the one combination that cannot
// be honoured — a value the browser may read that is also kept from it — is
// answered by the compiler where the definition is written, rather than by a
// throw when it runs. Reading never varies with class, so the split is visible
// only here, at the declaration.
export type VariableDefinition<
  TSchema extends StandardSchemaV1 = StandardSchemaV1,
> =
  | (VariableOptions<TSchema> & {
      // class decides confidentiality and delivery together. Reading never
      // varies with it, so reclassifying a key never edits a call site.
      class: "plain";

      // client marks a value the browser may read, which only a plaintext
      // value can be.
      client?: boolean;
    })
  | (VariableOptions<TSchema> & {
      class: ConfidentialClass;
      client?: false;
    });

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

// owner records the definitions file each key was claimed by. It is
// process-global because definitions may be spread across files and a duplicate
// key is only observable from above all of them. It records the file rather
// than the bare fact of a claim because a file re-executing — every reload
// under `ocel dev` — is restating its own claim, not making a second one. The
// cost is that two claims written in one file read as one, which is the right
// trade for an invariant that survives a reload: a key belongs to one file.
const owner = new Map<string, string>();

export function validateDefinitions(
  definitions: Definitions,
  source: string,
): void {
  for (const [key, definition] of Object.entries(definitions)) {
    validateDefinition(key, definition, source);
    owner.set(key, source);
  }
}

function validateDefinition(
  key: string,
  definition: VariableDefinition,
  source: string,
): void {
  if (!KEY_PATTERN.test(key)) {
    throw new EnvDefinitionError(
      `'${key}' is not a usable variable name: use upper-case letters, digits and underscores, starting with a letter or underscore.`,
    );
  }
  const claimed = owner.get(key);
  if (claimed !== undefined && claimed !== source) {
    throw new EnvDefinitionError(
      `'${key}' is already declared in ${claimed}. A key may be defined by exactly one definitions file.`,
    );
  }
  // A definition's own type refuses client access on a confidential class, so
  // this answers only a caller the compiler never saw.
  const variableClass: VariableClass = definition.class;
  if (definition.client && variableClass !== "plain") {
    throw new EnvDefinitionError(
      `'${key}' is client-accessible and class '${variableClass}'. A value the browser can read cannot be an encrypted class.`,
    );
  }
  if (
    (BARE_KEY_CLASSES.has(variableClass) || definition.client) &&
    RESERVED_PREFIXES.some((prefix) => key.startsWith(prefix))
  ) {
    throw new EnvDefinitionError(
      `'${key}' starts with a reserved prefix (${RESERVED_PREFIXES.join(", ")}). A '${variableClass}' variable is delivered under its own name, so the platform would overwrite it.`,
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

// complaint is what a rejected value is allowed to be described as. A schema's
// message is computed from the value it rejected — the built-in ones quote the
// input and a custom one may say anything about it — so it survives only for
// the class whose values are plaintext by declaration. Scrubbing the value out
// of the message instead would be guesswork: a message may hold it transformed,
// so nothing derived from the value is forwarded for the other classes. The
// key and the folder travel beside this, which is what names the failing cell.
export function complaint(
  definition: VariableDefinition,
  message: string,
): string {
  if (definition.class === "plain") return message;
  return `withheld, because a '${definition.class}' value's schema message can quote the value itself`;
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
