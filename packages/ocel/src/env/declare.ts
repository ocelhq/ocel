import type { StandardJSONSchemaV1, StandardSchemaV1 } from "@standard-schema/spec";
import {
  type VariableCell,
  VariableProblem_Kind,
  VariableClass as WireClass,
  ValueType as WireValueType,
} from "../gen/proto/app/resources/v1/variables_pb.js";
import { rpc } from "../runtime/rpc.js";
import {
  complaint,
  type FlatDefinitions,
  type VariableClass,
  type VariableDefinition,
} from "./definition.js";
import { type ParseResult, parse } from "./standard.js";

const WIRE_CLASS: Record<VariableClass, WireClass> = {
  plain: WireClass.PLAIN,
  sensitive: WireClass.SENSITIVE,
  secret: WireClass.SECRET,
};

export async function declareEnv(definitions: FlatDefinitions, source: string): Promise<void> {
  const groups = Object.values(
    Object.entries(definitions).reduce<
      Record<string, { key: string; required: boolean; description: string }>
    >((all, [, definition]) => {
      if (definition.group) {
        all[definition.group] = {
          key: definition.group,
          required: !definition.groupOptional,
          description: definition.groupDescription ?? "",
        };
      }
      return all;
    }, {}),
  );
  const { cells } = await rpc.resource.declareEnv({
    definitions: Object.entries(definitions).map(([key, definition]) => {
      const fallback = definition.schema ? parse(definition.schema, undefined) : undefined;
      return {
        key,
        class: WIRE_CLASS[definition.class],
        required: fallback?.ok !== true,
        folders: [...(definition.folders ?? [])],
        source,
        ...(definition.description === undefined ? {} : { description: definition.description }),
        ...(definition.group === undefined ? {} : { group: definition.group }),
        ...describeDefault(definition, fallback),
        ...describeValue(definition),
      };
    }),
    groups,
  });

  const problems = findInvalidValues(definitions, cells);
  if (problems.length > 0) {
    await rpc.resource.reportEnvProblems({ problems });
  }
}

const MAX_DEFAULT_BYTES = 1024;
const MAX_CHOICES = 100;
const MAX_CHOICE_BYTES = 256;

function byteLength(text: string): number {
  return new TextEncoder().encode(text).length;
}

function describeDefault(
  definition: VariableDefinition,
  fallback: ParseResult | undefined,
): { defaulted: true; defaultValue?: string } | Record<string, never> {
  if (!definition.schema || definition.class === "secret") return {};
  if (!fallback?.ok || fallback.value === undefined) return {};
  if (definition.class !== "plain") return { defaulted: true };
  const text = writeAsText(fallback.value);
  if (
    text === undefined ||
    byteLength(text) > MAX_DEFAULT_BYTES ||
    !readsAs(definition.schema, text, fallback.value)
  ) {
    return { defaulted: true };
  }
  return { defaulted: true, defaultValue: text };
}

function writeAsText(value: unknown): string | undefined {
  switch (typeof value) {
    case "string":
      return value;
    case "number":
    case "bigint":
    case "boolean":
      return String(value);
    default:
      return undefined;
  }
}

function probe(schema: StandardSchemaV1, text: string): { value: unknown } | undefined {
  try {
    const result = schema["~standard"].validate(text);
    if (result instanceof Promise) {
      result.catch(() => {});
      return undefined;
    }
    return result.issues ? undefined : { value: result.value };
  } catch {
    return undefined;
  }
}

function readsAs(schema: StandardSchemaV1, text: string, value: unknown): boolean {
  const result = probe(schema, text);
  return result !== undefined && Object.is(result.value, value);
}

function describeValue(
  definition: VariableDefinition,
): { valueType: WireValueType } | { choices: string[] } | Record<string, never> {
  const schema = definition.schema;
  if (!schema || definition.class === "secret") return {};
  if (readsAs(schema, "true", true) && readsAs(schema, "false", false)) {
    return { valueType: WireValueType.BOOLEAN };
  }
  const input = readInputJSONSchema(schema);
  if (input?.type === "integer") return { valueType: WireValueType.INTEGER };
  if (input?.type === "number") return { valueType: WireValueType.NUMBER };
  const choices = input?.enum;
  if (
    Array.isArray(choices) &&
    choices.length > 0 &&
    choices.length <= MAX_CHOICES &&
    choices.every(
      (choice): choice is string =>
        typeof choice === "string" &&
        byteLength(choice) <= MAX_CHOICE_BYTES &&
        probe(schema, choice) !== undefined,
    )
  ) {
    return { choices: [...new Set(choices)] };
  }
  return {};
}

function readInputJSONSchema(schema: StandardSchemaV1): Record<string, unknown> | undefined {
  const props: Partial<StandardJSONSchemaV1.Props> = schema["~standard"];
  if (!props.jsonSchema) return undefined;
  try {
    return props.jsonSchema.input({ target: "draft-2020-12" });
  } catch {
    return undefined;
  }
}

interface ReportedProblem {
  key: string;
  folder: string;
  kind: VariableProblem_Kind;
  detail: string;
}

function findInvalidValues(
  definitions: FlatDefinitions,
  cells: readonly VariableCell[],
): ReportedProblem[] {
  const problems: ReportedProblem[] = [];

  for (const [key, definition] of Object.entries(definitions)) {
    if (definition.class === "secret" || !definition.schema) continue;

    for (const cell of cells) {
      if (cell.key !== key) continue;
      const result = parse(definition.schema, cell.value);
      if (!result.ok) {
        problems.push(
          problem(
            key,
            cell.folder,
            VariableProblem_Kind.INVALID,
            complaint(definition, result.message),
          ),
        );
      }
    }
  }

  return problems;
}

function problem(
  key: string,
  folder: string,
  kind: VariableProblem_Kind,
  detail: string,
): ReportedProblem {
  return { key, folder, kind, detail };
}
