export type Shard = { index: number; total: number };

export function parseShard(value: string | undefined): Shard | undefined {
  if (value === undefined) {
    return undefined;
  }
  const match = /^(\d+)\/(\d+)$/.exec(value.trim());
  if (!match) {
    throw new Error(`--shard must be index/total, got ${JSON.stringify(value)}`);
  }
  const index = Number(match[1]);
  const total = Number(match[2]);
  if (total < 1) {
    throw new Error(`--shard total must be at least 1, got ${total}`);
  }
  if (index < 1 || index > total) {
    throw new Error(`--shard index must be between 1 and ${total}, got ${index}`);
  }
  return { index, total };
}
