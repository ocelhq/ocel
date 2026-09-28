export const targetsSortKey = "invalidation#";

export const targetsAttribute = "value";

export const ledgerRoot = "ledger";

export interface DynamoLike {
  send(command: any): Promise<any>;
}

export interface DynamoCommands {
  GetItemCommand: new (input: any) => any;
}

export function ledgerPartition(project: string): string {
  return `${ledgerRoot}#${project.replaceAll("%", "%25").replaceAll("#", "%23")}`;
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
  const noted = out?.Item?.[targetsAttribute]?.S;
  if (typeof noted !== "string") return [];
  const distributions = JSON.parse(noted);
  return Array.isArray(distributions) ? distributions.filter((d) => typeof d === "string") : [];
}

export async function targetsOf(
  dynamo: DynamoLike,
  commands: DynamoCommands,
  table: string,
  bootstrapTier: string,
  project: string,
): Promise<string[]> {
  const [wildcard, owned] = await Promise.all([
    notedAt(dynamo, commands, table, ledgerRoot),
    notedAt(dynamo, commands, table, ledgerPartition(project)),
  ]);
  const targets = [...new Set([...wildcard, ...owned])].sort();
  if (targets.length === 0) {
    console.warn(
      `ocel: the ${bootstrapTier} ledger names no front to invalidate for ${project}, so its raised tags reach nothing`,
    );
  }
  return targets;
}
