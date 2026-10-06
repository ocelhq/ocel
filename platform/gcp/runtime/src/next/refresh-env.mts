export interface RefreshEnv {
  url: string;
  queue: string;
  account: string;
  isrPrefix: string;
  secret: string;
  target?: string;
  certsUrl?: string;
}

const tasksEmulatorHost = "host.docker.internal";

export function readIdTokenCertsUrl(env: NodeJS.ProcessEnv): string | undefined {
  const value = env.OCEL_ID_TOKEN_CERTS_URL;
  if (!value) return undefined;
  const refused = new Error(
    `ocel: OCEL_ID_TOKEN_CERTS_URL names ${value}, and only the floci lane's tasks emulator on ${tasksEmulatorHost}, at OCEL_TASKS_ENDPOINT, may stand in for Google's token signing keys`,
  );
  const tasks = env.OCEL_TASKS_ENDPOINT;
  if (!tasks) throw refused;
  let certs: URL;
  let tasksOrigin: string;
  try {
    certs = new URL(value);
    tasksOrigin = new URL(tasks).origin;
  } catch {
    throw refused;
  }
  if (certs.hostname !== tasksEmulatorHost || certs.origin !== tasksOrigin) throw refused;
  return value;
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
  return {
    url,
    queue,
    account,
    isrPrefix,
    secret,
    target: env.OCEL_REFRESH_TARGET || undefined,
    certsUrl: readIdTokenCertsUrl(env),
  };
}
