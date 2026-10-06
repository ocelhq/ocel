export interface RefreshEnv {
  url: string;
  queue: string;
  account: string;
  isrPrefix: string;
  secret: string;
  target?: string;
}

export function readRefreshEnv(
  env: NodeJS.ProcessEnv,
  secret: string | undefined,
): RefreshEnv | undefined {
  const url = env.OCEL_REFRESH_URL;
  if (!url) return undefined;
  const queue = env.OCEL_REFRESH_QUEUE;
  if (!queue) throw new Error("ocel: OCEL_REFRESH_URL is set but OCEL_REFRESH_QUEUE is not");
  const account = env.OCEL_REFRESH_ACCOUNT;
  if (!account) throw new Error("ocel: OCEL_REFRESH_URL is set but OCEL_REFRESH_ACCOUNT is not");
  const isrPrefix = env.OCEL_ISR_PREFIX;
  if (!isrPrefix) throw new Error("ocel: OCEL_REFRESH_URL is set but OCEL_ISR_PREFIX is not");
  if (!secret) throw new Error("ocel: OCEL_REFRESH_URL is set but OCEL_REFRESH_SECRET is not");
  return { url, queue, account, isrPrefix, secret, target: env.OCEL_REFRESH_TARGET || undefined };
}
