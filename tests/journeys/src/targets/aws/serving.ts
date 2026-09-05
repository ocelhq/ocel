import type { Fetch } from "../../contract";

const EDGE_HEADER = "x-ocel-edge";
const STABLE_EDGE_ANSWERS = 6;

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
  request: Fetch,
  urls: Map<string, string>,
  { timeoutMs, intervalMs, now, sleep }: ServingWait,
): Promise<Record<string, ServingReport>> {
  const began = now();
  const deadline = began + timeoutMs;
  const served: Record<string, ServingReport> = {};

  for (const [app, baseUrl] of urls) {
    const host = new URL(baseUrl).host;
    let attempts = 0;
    let last = "no attempt completed";
    let edged = 0;
    let edgedSince = began;
    let edgedStatus = 0;
    while (true) {
      attempts += 1;
      try {
        const response = await request(`${baseUrl}/health`);
        if (response.ok) {
          served[app] = { host, waitedMs: now() - began, attempts, status: response.status };
          break;
        }
        last = `last status ${response.status}`;
        if (response.headers.get(EDGE_HEADER)) {
          if (edged === 0) {
            edgedSince = now();
          }
          edged += 1;
          edgedStatus = response.status;
        } else {
          edged = 0;
        }
      } catch (refused) {
        last = (refused as Error).message;
        edged = 0;
      }
      if (edged >= STABLE_EDGE_ANSWERS) {
        throw new Error(
          `${host} is served by the ocel edge, which answered ${edgedStatus} for ${edged} ` +
            `consecutive attempts over ${Math.round((now() - edgedSince) / 1000)}s: the app ` +
            `behind the edge is broken, not still propagating`,
        );
      }
      if (now() >= deadline) {
        throw new Error(
          `${host} served no 2xx in the ${Math.round(timeoutMs / 1000)}s after the edge ` +
            `said it was serving, across ${attempts} attempts: ${last}`,
        );
      }
      await sleep(intervalMs);
    }
  }

  return served;
}
