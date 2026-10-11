export const targetsSortKey = "invalidation#";

export const targetsAttribute = "value";

export const routerPartition = "routers#cloudfront";

export const cloudflarePartition = "routers#cloudflare";

export interface DynamoLike {
  send(command: any): Promise<any>;
}

export interface DynamoCommands {
  GetItemCommand: new (input: any) => any;
}

function escaped(project: string): string {
  return project.replaceAll("%", "%25").replaceAll("#", "%23");
}

export function projectPartition(project: string): string {
  return `${routerPartition}#${escaped(project)}`;
}

export function cloudflareProjectPartition(project: string): string {
  return `${cloudflarePartition}#${escaped(project)}`;
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
  project: string,
): Promise<string[]> {
  const [wildcard, owned] = await Promise.all([
    notedAt(dynamo, commands, table, routerPartition),
    notedAt(dynamo, commands, table, projectPartition(project)),
  ]);
  return [...new Set([...wildcard, ...owned])].sort();
}

export function cloudflareTargetsOf(
  dynamo: DynamoLike,
  commands: DynamoCommands,
  table: string,
  project: string,
): Promise<string[]> {
  return notedAt(dynamo, commands, table, cloudflareProjectPartition(project)).then((urls) =>
    [...new Set(urls)].sort(),
  );
}
