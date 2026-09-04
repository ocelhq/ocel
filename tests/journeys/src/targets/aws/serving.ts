export type ServingReport = {
  host: string;
  waitedMs: number;
  attempts: number;
  status: number;
};

export type ServingWait = {
  timeoutMs: number;
  intervalMs: number;
  now: () => number;
  sleep: (ms: number) => Promise<void>;
};

export async function awaitServing(
  request: typeof fetch,
  urls: Map<string, string>,
  { timeoutMs, intervalMs, now, sleep }: ServingWait,
): Promise<Record<string, ServingReport>> {
  const began = now();
  const deadline = began + timeoutMs;
  const served: Record<string, ServingReport> = {};

  for (const [app, baseUrl] of urls) {
    const host = new URL(baseUrl).host;
    let attempts = 0;
    while (true) {
      attempts += 1;
      try {
        const response = await request(`${baseUrl}/health`);
        served[app] = { host, waitedMs: now() - began, attempts, status: response.status };
        break;
      } catch (refused) {
        if (now() >= deadline) {
          throw new Error(
            `${host} answered no request in the ${Math.round(timeoutMs / 1000)}s after the edge ` +
              `said it was serving, across ${attempts} attempts: ${(refused as Error).message}`,
          );
        }
        await sleep(intervalMs);
      }
    }
  }

  return served;
}
