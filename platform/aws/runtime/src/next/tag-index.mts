import type { TagRecordUpdate } from "@framework/next-runtime/use-cache-store";

export type TagAttribute = { S: string } | { N: string };

export interface TagUpdateItem {
  TableName: string;
  Key: Record<string, TagAttribute>;
  ConditionExpression: string;
  UpdateExpression: string;
  ExpressionAttributeValues: Record<string, TagAttribute>;
}

const sortKeyWidth = 15;

export const tagSortKey = (at: number) => String(Math.round(at)).padStart(sortKeyWidth, "0");

export function tagRecordUpdate(
  table: string,
  namespace: string,
  tag: string,
  record: TagRecordUpdate,
): TagUpdateItem {
  const advancing = record.expired !== undefined ? "expired" : "stale";
  const sets = ["tag = :tag", "gsi1pk = :ns", "gsi1sk = :writtenAt"];
  const values: Record<string, TagAttribute> = {
    ":tag": { S: tag },
    ":ns": { S: namespace },
    ":writtenAt": { S: tagSortKey(record.writtenAt) },
  };
  for (const field of ["expired", "stale"] as const) {
    const value = record[field];
    if (value === undefined) continue;
    sets.push(`${field} = :${field}`);
    values[`:${field}`] = { N: String(value) };
  }

  return {
    TableName: table,
    Key: { pk: { S: `${namespace}${tag}` }, sk: { S: "#META" } },
    ConditionExpression: `attribute_not_exists(${advancing}) OR ${advancing} < :${advancing}`,
    UpdateExpression: `SET ${sets.join(", ")}`,
    ExpressionAttributeValues: values,
  };
}

export function isGuardRejection(err: any): boolean {
  return err?.name === "ConditionalCheckFailedException";
}

const KIND = "isr";
const KEY = "#";
const FIELD = "--";
const SLUG = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/;
const RELEASE = /^r[0-9a-f]{8}$/;

function coordinate(
  env: string,
  project: string,
  app: string,
  release: string,
): [string, string, string, string] | null {
  if (![env, project, app].every((f) => SLUG.test(f) && !f.includes(FIELD))) return null;
  return RELEASE.test(release) ? [env, project, app, release] : null;
}

export function tagNamespace(isrPrefix: string): string | null {
  const segments = isrPrefix.split("/");
  if (segments.length !== 5 || segments[4] !== KIND) return null;
  const facts = coordinate(segments[0]!, segments[1]!, segments[2]!, segments[3]!);
  if (facts === null) return null;
  const [env, project, app, release] = facts;
  const stack = [env, app, release].join(FIELD);
  return `PROJECT${KEY}${project}${KEY}STACK${KEY}${stack}${KEY}TAG${KEY}`;
}

export function isrPrefixOf(namespace: string): string | null {
  const tokens = namespace.split(KEY);
  if (tokens.length !== 6) return null;
  if (tokens[0] !== "PROJECT" || tokens[2] !== "STACK" || tokens[4] !== "TAG" || tokens[5] !== "") {
    return null;
  }
  const fields = tokens[3]!.split(FIELD);
  if (fields.length !== 3) return null;
  const facts = coordinate(fields[0]!, tokens[1]!, fields[1]!, fields[2]!);
  if (facts === null) return null;
  const [env, project, app, release] = facts;
  return [env, project, app, release, KIND].join("/");
}
