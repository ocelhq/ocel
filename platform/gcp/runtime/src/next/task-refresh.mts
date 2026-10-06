import { createHash } from "node:crypto";
import type { Refresh, ScheduleRefresh } from "@framework/next-runtime/refresh";
import { newMetadataToken } from "./metadata-token.mjs";

export interface TaskRefreshOptions {
  queue: string;
  account: string;
  url: string;
  isrPrefix: string;
  endpoint?: string;
  fetch?: typeof fetch;
  metadataOrigin?: string;
  now?: () => number;
  random?: () => number;
  sleep?: (ms: number) => Promise<void>;
}

const attempts = 3;
const attemptTimeoutMs = 2_000;
const queuedMemoryMs = 5 * 60_000;
const queuedMemoryNames = 10_000;
const dispatchDeadline = "60s";

class Refused extends Error {}

export function newTaskRefresh(options: TaskRefreshOptions): ScheduleRefresh {
  const doFetch = options.fetch ?? globalThis.fetch;
  const now = options.now ?? Date.now;
  const random = options.random ?? Math.random;
  const sleep =
    options.sleep ?? ((ms: number) => new Promise<void>((done) => setTimeout(done, ms)));
  const origin = (options.endpoint ?? "https://cloudtasks.googleapis.com").replace(/\/$/, "");
  const metadata = options.endpoint
    ? undefined
    : newMetadataToken({
        service: "Cloud Tasks",
        fetch: (input, init) =>
          doFetch(input, { ...init, signal: AbortSignal.timeout(attemptTimeoutMs) }),
        metadataOrigin: options.metadataOrigin,
        now,
      });
  const inflight = new Map<string, Promise<void>>();
  const queued = new Map<string, number>();

  function failure(refresh: Refresh, cause: string): Error {
    return new Error(`ocel: Cloud Tasks did not queue the refresh of ${refresh.url}: ${cause}`);
  }

  async function createTask(name: string, refresh: Refresh): Promise<void> {
    const body = JSON.stringify({
      task: {
        name,
        dispatchDeadline,
        httpRequest: {
          url: options.url,
          httpMethod: "POST",
          headers: { "Content-Type": "application/json" },
          body: Buffer.from(JSON.stringify({ isrPrefix: options.isrPrefix, refresh })).toString(
            "base64",
          ),
          oidcToken: { serviceAccountEmail: options.account, audience: options.url },
        },
      },
    });
    let renewed = false;
    let cause = "";
    for (let attempt = 1; attempt <= attempts; attempt++) {
      if (attempt > 1) await sleep(random() * Math.min(400, 100 * 2 ** (attempt - 1)));
      try {
        const headers: Record<string, string> = { "Content-Type": "application/json" };
        if (metadata) headers.Authorization = `Bearer ${await metadata.token()}`;
        const res = await doFetch(`${origin}/v2/${options.queue}/tasks`, {
          method: "POST",
          headers,
          body,
          signal: AbortSignal.timeout(attemptTimeoutMs),
        });
        if (res.ok) return;
        cause = String(res.status);
        if (res.status === 409) {
          const reply = (await res.json().catch(() => undefined)) as
            | { error?: { status?: string } }
            | undefined;
          if (reply?.error?.status !== "ABORTED") return;
          continue;
        }
        if (res.status === 401 && metadata) {
          metadata.forget();
          if (renewed) throw new Refused(failure(refresh, cause).message);
          renewed = true;
          continue;
        }
        if (res.status === 408 || res.status === 429 || res.status >= 500) continue;
        throw new Refused(failure(refresh, cause).message);
      } catch (error) {
        if (error instanceof Refused) throw error;
        cause = error instanceof Error ? error.name : "failed";
      }
    }
    throw failure(refresh, cause);
  }

  function remember(id: string): void {
    queued.delete(id);
    queued.set(id, now() + queuedMemoryMs);
    if (queued.size > queuedMemoryNames) {
      const oldest = queued.keys().next().value;
      if (oldest !== undefined) queued.delete(oldest);
    }
  }

  return (refresh) => {
    const id = createHash("sha256")
      .update(`${options.isrPrefix}\0${refresh.key}\0${String(refresh.lastModified)}`)
      .digest("hex");
    const until = queued.get(id);
    if (until !== undefined) {
      if (now() < until) return Promise.resolve();
      queued.delete(id);
    }
    const running = inflight.get(id);
    if (running) return running;
    const creating = createTask(`${options.queue}/tasks/${id}`, refresh)
      .then(() => remember(id))
      .finally(() => inflight.delete(id));
    inflight.set(id, creating);
    return creating;
  };
}
