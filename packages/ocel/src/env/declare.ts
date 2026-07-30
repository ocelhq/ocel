import {
  VariableClass as WireClass,
  VariableProblem_Kind,
  type VariableCell,
} from "../gen/proto/resources/v1/env_pb.js";
import { rpc } from "../utils/rpc.js";
import {
  isRequired,
  parse,
  type Definitions,
  type VariableClass,
} from "./definition.js";

const WIRE_CLASS: Record<VariableClass, WireClass> = {
  plain: WireClass.PLAIN,
  sensitive: WireClass.SENSITIVE,
  secret: WireClass.SECRET,
};

// declareEnv sends every definition of one call, validates the cells the
// store answers with, and reports what a deploy must not proceed on. The
// verdict is formed here because the schemas are here: they are live objects
// in this language and could not travel to the CLI.
export async function declareEnv(definitions: Definitions): Promise<void> {
  const { cells } = await rpc.resource.declareEnv({
    definitions: Object.entries(definitions).map(([key, definition]) => ({
      key,
      class: WIRE_CLASS[definition.class],
      clientAccessible: definition.client === true,
      required: isRequired(definition),
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
    if (stored.length === 0) {
      if (isRequired(definition)) {
        problems.push(problem(key, "", VariableProblem_Kind.MISSING, ""));
      }
      continue;
    }

    // A live cell arrives as presence without plaintext, so there is nothing
    // to check here; its schema is re-checked at runtime init instead, which
    // is required regardless because a live value can drift after a deploy.
    if (definition.class === "secret" || !definition.schema) continue;

    for (const cell of stored) {
      const result = parse(definition.schema, cell.value);
      if (!result.ok) {
        problems.push(
          problem(key, cell.folder, VariableProblem_Kind.INVALID, result.message),
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
