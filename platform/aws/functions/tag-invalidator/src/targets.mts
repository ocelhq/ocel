export const targetsSortKey = "META#invalidation";

export const targetsAttribute = "distributions";

export const partitionPrefix = "EDGELEDGER#";

export interface DynamoLike {
  send(command: any): Promise<any>;
}

export interface DynamoCommands {
  GetItemCommand: new (input: any) => any;
}

export function bootstrapPartition(bootstrapTier: string): string {
  return `${partitionPrefix}${bootstrapTier}`;
}

export function ledgerPartition(bootstrapTier: string, project: string): string {
  return `${bootstrapPartition(bootstrapTier)}/${project}`;
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
  const distributions = out?.Item?.[targetsAttribute]?.SS;
  return Array.isArray(distributions) ? distributions : [];
}

export async function targetsOf(
  dynamo: DynamoLike,
  commands: DynamoCommands,
  table: string,
  bootstrapTier: string,
  project: string,
): Promise<string[]> {
  const [wildcard, owned] = await Promise.all([
    notedAt(dynamo, commands, table, bootstrapPartition(bootstrapTier)),
    notedAt(dynamo, commands, table, ledgerPartition(bootstrapTier, project)),
  ]);
  const targets = [...new Set([...wildcard, ...owned])].sort();
  if (targets.length === 0) {
    console.warn(
      `ocel: the ${bootstrapTier} ledger names no front to invalidate for ${project}, so its raised tags reach nothing`,
    );
  }
  return targets;
}
