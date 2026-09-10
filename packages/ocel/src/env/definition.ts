import type { StandardSchemaV1 } from "@standard-schema/spec";
import { EnvDefinitionError } from "./errors.js";
import { scopeProblem } from "./scope.js";
import { parse } from "./standard.js";

export type VariableClass = "plain" | "sensitive" | "secret";

export type ConfidentialClass = Exclude<VariableClass, "plain">;

interface VariableOptions<TSchema extends StandardSchemaV1 = StandardSchemaV1> {
  schema?: TSchema;

  folders?: readonly string[];

  description?: string;
}

export type VariableDefinition<TSchema extends StandardSchemaV1 = StandardSchemaV1> =
  | (VariableOptions<TSchema> & {
      class: "plain";

      client?: boolean;
    })
  | (VariableOptions<TSchema> & {
      class: ConfidentialClass;
      client?: false;
    });

export interface GroupOptions {
  readonly optional?: boolean;

  readonly description?: string;
}

export interface GroupDefinition<
  TDefinitions extends Definitions = Definitions,
  TOptional extends boolean = boolean,
> {
  readonly definitions: TDefinitions;
  readonly optional: TOptional;
  readonly description?: string;
}

type IsOptional<TOptions extends GroupOptions> = TOptions["optional"] extends true ? true : false;

export type Definitions = Record<string, VariableDefinition>;

export type EnvDefinitions = Record<string, VariableDefinition | GroupDefinition>;

export type FlatDefinition = VariableDefinition & {
  readonly group?: string;
  readonly groupOptional?: boolean;
  readonly groupDescription?: string;
};

export type FlatDefinitions = Record<string, FlatDefinition>;

/** A set of variables an app takes together: every member is reached through the group and
 * nowhere else, and `{ optional: true }` reads the whole group as `undefined` until a value
 * is delivered for one of its members. */
export function group<
  const TDefinitions extends Definitions,
  const TOptions extends GroupOptions = GroupOptions,
>(
  definitions: TDefinitions,
  options?: TOptions,
): GroupDefinition<TDefinitions, IsOptional<TOptions>> {
  return {
    definitions,
    optional: (options?.optional === true) as IsOptional<TOptions>,
    ...(options?.description === undefined ? {} : { description: options.description }),
  };
}

export function flattenDefinitions(definitions: EnvDefinitions): FlatDefinitions {
  const flat: FlatDefinitions = {};
  for (const [key, definition] of Object.entries(definitions)) {
    if ("definitions" in definition) {
      for (const [member, nested] of Object.entries(definition.definitions)) {
        if ("definitions" in nested)
          throw new EnvDefinitionError(
            `group '${key}' nests another group. Groups nest one level only.`,
          );
        claim(flat, member, key);
        flat[member] = {
          ...nested,
          group: key,
          groupOptional: definition.optional,
          groupDescription: definition.description,
        };
      }
      continue;
    }
    claim(flat, key);
    flat[key] = definition;
  }
  return flat;
}

function claim(flat: FlatDefinitions, key: string, group?: string): void {
  const held = flat[key];
  if (!held) return;
  const inside = [held.group, group].filter((name): name is string => name !== undefined);
  if (inside.length === 0) {
    throw new EnvDefinitionError(`'${key}' is declared twice in the same call.`);
  }
  throw new EnvDefinitionError(
    `'${key}' is declared twice in the same call, once inside ${inside
      .map((name) => `group '${name}'`)
      .join(" and once inside ")}.`,
  );
}

const LIVE_CLASSES: ReadonlySet<VariableClass> = new Set(["secret"]);

export function isLive(definition: VariableDefinition): boolean {
  return LIVE_CLASSES.has(definition.class);
}

const BARE_KEY_CLASSES: ReadonlySet<VariableClass> = new Set(["plain"]);

const RESERVED_PREFIXES = ["OCEL_"];

export const URL_KEY = "OCEL_URL";

export const CLIENT_URL_KEY = "NEXT_PUBLIC_OCEL_URL";

const RESERVED_KEYS = [URL_KEY, CLIENT_URL_KEY];

const KEY_PATTERN = /^[A-Z_][A-Z0-9_]*$/;

export function isUsableKey(key: string): boolean {
  return KEY_PATTERN.test(key);
}

const owner = new Map<string, string>();

export function validateDefinitions(definitions: EnvDefinitions, source: string): FlatDefinitions {
  for (const [key, definition] of Object.entries(definitions)) {
    if ("definitions" in definition) validateGroup(key, definition);
  }
  const flat = flattenDefinitions(definitions);
  for (const [key, definition] of Object.entries(flat)) {
    validateDefinition(key, definition, source);
    owner.set(key, source);
  }
  return flat;
}

function validateGroup(key: string, definition: GroupDefinition): void {
  if (key === "" || key.includes("#") || /\p{Cc}/u.test(key)) {
    throw new EnvDefinitionError(
      `'${key}' is not a usable group name: a group name is one line and has no '#'.`,
    );
  }
  if (Object.keys(definition.definitions).length === 0) {
    throw new EnvDefinitionError(
      `'${key}' declares no variables. A group holds the variables an app takes together, so it holds at least one.`,
    );
  }
  const problem = descriptionProblem(definition.description);
  if (problem) {
    throw new EnvDefinitionError(`'${key}' has an unusable description: ${problem}`);
  }
  const unsatisfiable = unsharedScopes(definition.definitions);
  if (unsatisfiable) {
    throw new EnvDefinitionError(
      `'${key}' is read as one, and no folder satisfies every member: ${unsatisfiable}.`,
    );
  }
}

function unsharedScopes(definitions: Definitions): string | undefined {
  let shared: readonly string[] | undefined;
  const scoped: string[] = [];
  for (const [key, definition] of Object.entries(definitions)) {
    const folders = definition.folders;
    if (!folders || folders.length === 0) continue;
    scoped.push(`${key} (${folders.join(", ")})`);
    shared = shared === undefined ? folders : shared.filter((folder) => folders.includes(folder));
  }
  if (shared === undefined || shared.length > 0) return undefined;
  return scoped.join(", ");
}

function descriptionProblem(description: string | undefined): string {
  if (description === undefined) return "";
  if (new TextEncoder().encode(description).length > 120) {
    return "a description is at most 120 bytes.";
  }
  if (/\p{Cc}/u.test(description)) {
    return "a description is one line and has no control characters.";
  }
  return "";
}

function validateDefinition(key: string, definition: VariableDefinition, source: string): void {
  if (!KEY_PATTERN.test(key)) {
    throw new EnvDefinitionError(
      `'${key}' is not a usable variable name: use upper-case letters, digits and underscores, starting with a letter or underscore.`,
    );
  }
  if (RESERVED_KEYS.includes(key)) {
    throw new EnvDefinitionError(
      `'${key}' is written by Ocel for every app, from the hostname the deploy serves it on, so a declared one would be overwritten before anything read it. Read it as \`deployment.url\` from 'ocel/env'.`,
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
  const described = descriptionProblem(definition.description);
  if (described) {
    throw new EnvDefinitionError(`'${key}' has an unusable description: ${described}`);
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

export function complaint(definition: VariableDefinition, message: string): string {
  if (definition.class === "plain") return message;
  return `withheld, because a '${definition.class}' value's schema message can quote the value itself`;
}
