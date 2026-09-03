import type { StandardSchemaV1 } from "@standard-schema/spec";
import { EnvDefinitionError } from "./errors.js";

export type ParseResult =
  | { ok: true; value: unknown }
  | { ok: false; message: string };

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
