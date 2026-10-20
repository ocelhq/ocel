import type { TagRecord } from "@platform/edge-contract/purge";
import {
  fieldSeparator,
  isStorageCoordinate,
  type StorageCoordinate,
} from "@platform/edge-contract/storage-key";

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
  record: TagRecord & { writtenAt: number },
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

const KEY = "#";

export function tagNamespace(coordinate: StorageCoordinate): string | null {
  if (!isStorageCoordinate(coordinate)) return null;
  const { project, env, app, release } = coordinate;
  const stack = [env, app, release].join(fieldSeparator);
  return `PROJECT${KEY}${project}${KEY}STACK${KEY}${stack}${KEY}TAG${KEY}`;
}

export function parseTagNamespace(namespace: string): StorageCoordinate | null {
  const tokens = namespace.split(KEY);
  if (tokens.length !== 6) return null;
  if (tokens[0] !== "PROJECT" || tokens[2] !== "STACK" || tokens[4] !== "TAG" || tokens[5] !== "") {
    return null;
  }
  const fields = tokens[3]!.split(fieldSeparator);
  if (fields.length !== 3) return null;
  const coordinate = { project: tokens[1]!, env: fields[0]!, app: fields[1]!, release: fields[2]! };
  return isStorageCoordinate(coordinate) ? coordinate : null;
}
