import { createHmac } from "node:crypto";
import type { Refresh, ScheduleRefresh } from "@framework/next-runtime/refresh";
import { newMetadataToken } from "./metadata-token.mjs";
import { refreshSignatureHeader, signRefreshTask } from "./refresh-signature.mjs";

export interface RefreshQueueOptions {
  queue: string;
  account: string;
  url: string;
  isrPrefix: string;
  secret: string;
  endpoint?: string;
  fetch?: typeof fetch;
  metadataOrigin?: string;
  now?: () => number;
  random?: () => number;
  sleep?: (ms: number) => Promise<void>;
}

const attempts = 3;
const attemptTimeoutMs = 2_000;
const deadlineMs = 8_000;
const queuedTaskTtlMs = 5 * 60_000;
const maxQueuedTaskCount = 10_000;
const dispatchDeadline = "60s";

class Refused extends Error {}

function within<T>(promise: Promise<T>, ms: number): Promise<T> {
  if (ms >= attemptTimeoutMs) return promise;
  let timer: NodeJS.Timeout | undefined;
  const expiry = new Promise<never>((_, reject) => {
    timer = setTimeout(
      () =>
        reject(new Error(`ocel: the metadata server gave no Cloud Tasks token within ${ms} ms`)),
      Math.max(ms, 0),
    );
  });
  return Promise.race([promise, expiry]).finally(() => clearTimeout(timer));
}

export function newRefreshQueue(options: RefreshQueueOptions): ScheduleRefresh {
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
        fetch: doFetch,
        timeoutMs: attemptTimeoutMs,
        metadataOrigin: options.metadataOrigin,
        now,
      });
  const inflight = new Map<string, Promise<void>>();
  const queued = new Map<string, number>();

  function failure(refresh: Refresh, cause: string): Error {
    return new Error(`ocel: Cloud Tasks did not queue the refresh of ${refresh.url}: ${cause}`);
  }

  async function createTask(name: string, refresh: Refresh): Promise<void> {
    const payload = Buffer.from(JSON.stringify({ isrPrefix: options.isrPrefix, refresh }));
    const body = JSON.stringify({
      task: {
        name,
        dispatchDeadline,
        httpRequest: {
          url: options.url,
          httpMethod: "POST",
          headers: {
            "Content-Type": "application/json",
            [refreshSignatureHeader]: signRefreshTask(options.secret, payload),
          },
          body: payload.toString("base64"),
          oidcToken: { serviceAccountEmail: options.account, audience: options.url },
        },
      },
    });
    const deadline = now() + deadlineMs;
    let renewed = false;
    let cause = "";
    for (let attempt = 1; attempt <= attempts; attempt++) {
      if (attempt > 1) await sleep(random() * Math.min(400, 100 * 2 ** (attempt - 1)));
      if (now() >= deadline) break;
      let timer: NodeJS.Timeout | undefined;
      try {
        const headers: Record<string, string> = { "Content-Type": "application/json" };
        if (metadata) {
          headers.Authorization = `Bearer ${await within(metadata.token(), deadline - now())}`;
        }
        const left = deadline - now();
        if (left <= 0) break;
        const controller = new AbortController();
        timer = setTimeout(() => controller.abort(), Math.min(attemptTimeoutMs, left));
        const res = await doFetch(`${origin}/v2/${options.queue}/tasks`, {
          method: "POST",
          headers,
          body,
          signal: controller.signal,
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
        cause = error instanceof Error ? error.message : "failed";
      } finally {
        clearTimeout(timer);
      }
    }
    throw failure(refresh, cause);
  }

  function remember(id: string): void {
    queued.delete(id);
    queued.set(id, now() + queuedTaskTtlMs);
    if (queued.size > maxQueuedTaskCount) {
      const oldest = queued.keys().next().value;
      if (oldest !== undefined) queued.delete(oldest);
    }
  }

  return (refresh) => {
    const id = createHmac("sha256", options.secret)
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
