import {
  type VariableCell,
  VariableProblem_Kind,
  VariableClass as WireClass,
} from "../gen/proto/app/resources/v1/variables_pb.js";
import { rpc } from "../runtime/rpc.js";
import { complaint, type FlatDefinitions, isRequired, type VariableClass } from "./definition.js";
import { parse } from "./standard.js";

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
    definitions: Object.entries(definitions).map(([key, definition]) => ({
      key,
      class: WIRE_CLASS[definition.class],
      required: isRequired(definition),
      folders: [...(definition.folders ?? [])],
      source,
      ...(definition.description === undefined ? {} : { description: definition.description }),
      ...(definition.group === undefined ? {} : { group: definition.group }),
    })),
    groups,
  });

  const problems = findInvalidValues(definitions, cells);
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
