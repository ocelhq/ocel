import type { StandardSchemaV1 } from "@standard-schema/spec";
import { EnvDefinitionError } from "./errors.js";
import { scopeProblem } from "./scope.js";
import { parse } from "./standard.js";

export type VariableClass = "plain" | "sensitive" | "secret";

export type ConfidentialClass = Exclude<VariableClass, "plain">;

interface VariableOptions<TSchema extends StandardSchemaV1 = StandardSchemaV1> {
  schema?: TSchema;

  folders?: readonly string[];
}

export type VariableDefinition<
  TSchema extends StandardSchemaV1 = StandardSchemaV1,
> =
  | (VariableOptions<TSchema> & {
      class: "plain";

      client?: boolean;
    })
  | (VariableOptions<TSchema> & {
      class: ConfidentialClass;
      client?: false;
    });

export type Definitions = Record<string, VariableDefinition>;

const LIVE_CLASSES: ReadonlySet<VariableClass> = new Set(["secret"]);

export function isLive(definition: VariableDefinition): boolean {
  return LIVE_CLASSES.has(definition.class);
}

const BARE_KEY_CLASSES: ReadonlySet<VariableClass> = new Set(["plain"]);

const RESERVED_PREFIXES = ["OCEL_"];

const KEY_PATTERN = /^[A-Z_][A-Z0-9_]*$/;

export function isUsableKey(key: string): boolean {
  return KEY_PATTERN.test(key);
}

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
  const variableClass: VariableClass = definition.class;
  if (definition.client && variableClass !== "plain") {
    throw new EnvDefinitionError(
      `'${key}' is client-accessible and class '${variableClass}'. A value the browser can read cannot be an encrypted class.`,
    );
  }
  if (
    BARE_KEY_CLASSES.has(variableClass) &&
    RESERVED_PREFIXES.some((prefix) => key.startsWith(prefix))
  ) {
    throw new EnvDefinitionError(
      `'${key}' starts with a reserved prefix (${RESERVED_PREFIXES.join(", ")}). A '${variableClass}' variable is delivered under its own name, so Ocel would overwrite it.`,
    );
  }
  if (definition.client && !isRequired(definition)) {
    throw new EnvDefinitionError(
      `'${key}' is client-accessible and its schema accepts a missing value. A client value is inlined into the browser bundle at build time, so a default or an optional could not be told apart from a value the bundler never inlined.`,
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

export function isRequired(definition: VariableDefinition): boolean {
  if (!definition.schema) return true;
  return parse(definition.schema, undefined).ok === false;
}

export function complaint(
  definition: VariableDefinition,
  message: string,
): string {
  if (definition.class === "plain") return message;
  return `withheld, because a '${definition.class}' value's schema message can quote the value itself`;
}
