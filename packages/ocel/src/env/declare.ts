import {
  VariableClass as WireClass,
  VariableProblem_Kind,
  type VariableCell,
} from "../gen/proto/resources/v1/env_pb.js";
import { rpc } from "../utils/rpc.js";
import {
  complaint,
  isRequired,
  parse,
  type Definitions,
  type VariableClass,
  type VariableDefinition,
} from "./definition.js";

const WIRE_CLASS: Record<VariableClass, WireClass> = {
  plain: WireClass.PLAIN,
  sensitive: WireClass.SENSITIVE,
  secret: WireClass.SECRET,
};

export async function declareEnv(
  definitions: Definitions,
  source: string,
): Promise<void> {
  const { cells } = await rpc.resource.declareEnv({
    definitions: Object.entries(definitions).map(([key, definition]) => ({
      key,
      class: WIRE_CLASS[definition.class],
      clientAccessible: definition.client === true,
      required: isRequired(definition),
      folders: [...(definition.folders ?? [])],
      source,
    })),
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

function validate(
  definitions: Definitions,
  cells: readonly VariableCell[],
): ReportedProblem[] {
  const problems: ReportedProblem[] = [];

  for (const [key, definition] of Object.entries(definitions)) {
    const stored = cells.filter((c) => c.key === key);

    if (isRequired(definition)) {
      for (const folder of requiredFolders(definition)) {
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
