export const targetsSortKey = "META#invalidation";

export const targetsAttribute = "distributions";

export const partitionPrefix = "EDGELEDGER#";

export interface DynamoLike {
  send(command: any): Promise<any>;
}

export interface DynamoCommands {
  GetItemCommand: new (input: any) => any;
}

export function substratePartition(substrateClass: string): string {
  return `${partitionPrefix}${substrateClass}`;
}

export function ledgerPartition(substrateClass: string, project: string): string {
  return `${substratePartition(substrateClass)}/${project}`;
}

async function notedAt(
  dynamo: DynamoLike,
  commands: DynamoCommands,
  table: string,
  partition: string,
): Promise<string[]> {
  const out = await dynamo.send(
    new commands.GetItemCommand({
      TableName: table,
      ConsistentRead: true,
      Key: {
        pk: { S: partition },
        sk: { S: targetsSortKey },
      },
    }),
  );
  const held = out?.Item?.[targetsAttribute]?.SS;
  return Array.isArray(held) ? held : [];
}

export async function targetsOf(
  dynamo: DynamoLike,
  commands: DynamoCommands,
  table: string,
  substrateClass: string,
  project: string,
): Promise<string[]> {
  const [wildcard, owned] = await Promise.all([
    notedAt(dynamo, commands, table, substratePartition(substrateClass)),
    notedAt(dynamo, commands, table, ledgerPartition(substrateClass, project)),
  ]);
  const targets = [...new Set([...wildcard, ...owned])].sort();
  if (targets.length === 0) {
    console.warn(
      `ocel: the ${substrateClass} ledger names no front to invalidate for ${project}, so its raised tags reach nothing`,
    );
  }
  return targets;
}
