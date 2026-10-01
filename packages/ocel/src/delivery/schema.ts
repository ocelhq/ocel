import type { StandardJSONSchemaV1, StandardSchemaV1 } from "@standard-schema/spec";
import { describeIssues } from "../env/standard.js";

export function encodeJsonSchema(schema: StandardSchemaV1 | undefined): string {
  const converter = (schema as Partial<StandardJSONSchemaV1> | undefined)?.["~standard"]
    ?.jsonSchema;
  if (!converter) return "";
  try {
    return JSON.stringify(converter.input({ target: "draft-2020-12" }));
  } catch {
    return "";
  }
}

export type Validation = { ok: true; value: unknown } | { ok: false; message: string };

export async function validatePayload(
  schema: StandardSchemaV1,
  payload: unknown,
): Promise<Validation> {
  const result = await schema["~standard"].validate(payload);
  if (result.issues) {
    return { ok: false, message: describeIssues(result.issues) };
  }
  return { ok: true, value: result.value };
}
