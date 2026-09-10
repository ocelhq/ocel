import {
  type VariableCell,
  VariableProblem_Kind,
  VariableClass as WireClass,
} from "../gen/proto/app/resources/v1/variables_pb.js";
import { rpc } from "../utils/rpc.js";
import {
  complaint,
  type FlatDefinitions,
  isRequired,
  type VariableClass,
  type VariableDefinition,
} from "./definition.js";
import { parse } from "./standard.js";

const WIRE_CLASS: Record<VariableClass, WireClass> = {
  plain: WireClass.PLAIN,
  sensitive: WireClass.SENSITIVE,
  secret: WireClass.SECRET,
};

export async function declareEnv(
  definitions: FlatDefinitions,
  source: string,
  schemaSource: string,
): Promise<void> {
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
    definitions: Object.entries(definitions).map(([key, definition]) => ({
      key,
      class: WIRE_CLASS[definition.class],
      clientAccessible: definition.client === true,
      required: isRequired(definition),
      folders: [...(definition.folders ?? [])],
      source,
      schemaSource,
      hasSchema: definition.schema !== undefined,
      ...(definition.description === undefined ? {} : { description: definition.description }),
      ...(definition.group === undefined ? {} : { group: definition.group }),
    })),
    groups,
  });

  const problems = validate(definitions, cells);
  if (problems.length > 0) {
    await rpc.resource.reportEnvProblems({ problems });
  }
}

interface ReportedProblem {
  key: string;
  folder: string;
  kind: VariableProblem_Kind;
  detail: string;
}

function validate(definitions: FlatDefinitions, cells: readonly VariableCell[]): ReportedProblem[] {
  const problems: ReportedProblem[] = [];

  for (const [key, definition] of Object.entries(definitions)) {
    const stored = cells.filter((c) => c.key === key);

    if (isRequired(definition)) {
      for (const folder of requiredFolders(definition)) {
        if (
          definition.groupOptional &&
          definition.group &&
          !groupPresent(definitions, cells, definition.group, folder)
        )
          continue;
        if (!stored.some((c) => c.folder === folder)) {
          problems.push(problem(key, folder, VariableProblem_Kind.MISSING, ""));
        }
      }
    }

    if (definition.class === "secret" || !definition.schema) continue;

    for (const cell of stored) {
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

function groupPresent(
  definitions: FlatDefinitions,
  cells: readonly VariableCell[],
  group: string,
  folder: string,
): boolean {
  return Object.entries(definitions).some(
    ([key, definition]) => definition.group === group && held(cells, key, definition, folder),
  );
}

function held(
  cells: readonly VariableCell[],
  key: string,
  definition: VariableDefinition,
  folder: string,
): boolean {
  const at = (where: string) => cells.some((cell) => cell.key === key && cell.folder === where);
  const scope = definition.folders ?? [];
  if (scope.length > 0) {
    return folder !== "" && scope.includes(folder) && at(folder);
  }
  return (folder !== "" && at(folder)) || at("");
}

function requiredFolders(definition: VariableDefinition): readonly string[] {
  return definition.folders?.length ? definition.folders : [""];
}

function problem(
  key: string,
  folder: string,
  kind: VariableProblem_Kind,
  detail: string,
): ReportedProblem {
  return { key, folder, kind, detail };
}
