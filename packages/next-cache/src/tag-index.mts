export type TagAttribute = { S: string } | { N: string };

export interface TagUpdateItem {
  TableName: string;
  Key: Record<string, TagAttribute>;
  ConditionExpression: string;
  UpdateExpression: string;
  ExpressionAttributeValues: Record<string, TagAttribute>;
}

export interface TagRecordUpdate {
  stale?: number;
  expired?: number;
  writtenAt: number;
}

const sortKeyWidth = 15;

export const tagSortKey = (at: number) =>
  String(Math.round(at)).padStart(sortKeyWidth, "0");

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
    UpdateExpression: "SET " + sets.join(", "),
    ExpressionAttributeValues: values,
  };
}

export function isGuardRejection(err: any): boolean {
  return err?.name === "ConditionalCheckFailedException";
}
