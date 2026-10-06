import { refreshHeader } from "@framework/next-cache";

import type { Env } from "./env";
import { sqsFetch, sqsRegion } from "./signing";

export interface RevalidationMessage {
  v: 1;
  headers: Record<string, string>;
  expect: { header: string; value: string } | null;
  isrPrefix: string;
  routeId: string;
  routePath: string;
  lastModified: number;
  enqueuedAt: number;
}

export type RevalidationRoute = Omit<RevalidationMessage, "v" | "lastModified" | "enqueuedAt"> & {
  origin: string;
};

export function revalidationMessage(
  { origin: _origin, ...route }: RevalidationRoute,
  lastModified: number,
  enqueuedAt: number = Date.now(),
): RevalidationMessage {
  return {
    v: 1,
    ...route,
    headers: { ...route.headers, [refreshHeader]: String(lastModified) },
    lastModified,
    enqueuedAt,
  };
}

export interface RevalidationIds {
  MessageGroupId: string;
  MessageDeduplicationId: string;
}

const maxIdLength = 128;

export const revalidationRetryWindowMs = 30_000;

export const queuedRefreshDeadlineMs = 300_000;

function retryWindow(enqueuedAt: number): number {
  return Math.floor(enqueuedAt / revalidationRetryWindowMs);
}

async function sha256Hex(value: string): Promise<string> {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(value));
  return [...new Uint8Array(digest)].map((byte) => byte.toString(16).padStart(2, "0")).join("");
}

export async function revalidationIds(message: RevalidationMessage): Promise<RevalidationIds> {
  const group = `${message.isrPrefix}:${message.routePath}`;
  const hash = await sha256Hex(group);
  return {
    MessageGroupId:
      group.length <= maxIdLength
        ? group
        : `${group.slice(0, maxIdLength - hash.length - 1)}:${hash}`,
    MessageDeduplicationId: await sha256Hex(
      `${group}:${message.lastModified}:${retryWindow(message.enqueuedAt)}`,
    ),
  };
}

export type RevalidationSender = (message: RevalidationMessage, origin: string) => Promise<boolean>;

export const enqueueTimeoutMs = 1_000;

export function queueSender(
  queueUrl: string,
  send: typeof fetch,
  timeoutMs: number = enqueueTimeoutMs,
): RevalidationSender {
  return async (message) => {
    try {
      const ids = await revalidationIds(message);
      const response = await send(queueUrl, {
        method: "POST",
        headers: { "content-type": "application/x-www-form-urlencoded" },
        body: new URLSearchParams({
          Action: "SendMessage",
          Version: "2012-11-05",
          MessageBody: JSON.stringify(message),
          ...ids,
        }).toString(),
        signal: AbortSignal.timeout(timeoutMs),
      });
      response.body?.cancel();
      if (!response.ok) {
        console.warn(
          `ocel: the revalidation queue refused the message with ${response.status} — rendering through the origin instead`,
        );
      }
      return response.ok;
    } catch (error) {
      console.warn("ocel: could not send to the revalidation queue", error);
      return false;
    }
  };
}

export function cloudflareQueueSender(
  queue: Queue,
  timeoutMs: number = enqueueTimeoutMs,
): RevalidationSender {
  return async (message, origin) => {
    let budget: ReturnType<typeof setTimeout> | undefined;
    try {
      await Promise.race([
        queue.send(JSON.stringify({ ...message, origin }), { contentType: "text" }),
        new Promise<never>((_, reject) => {
          budget = setTimeout(() => reject(new Error("over its budget")), timeoutMs);
        }),
      ]);
      return true;
    } catch (error) {
      console.warn("ocel: could not send to the refresh queue", error);
      return false;
    } finally {
      clearTimeout(budget);
    }
  };
}

export function revalidationSender(
  env: Pick<
    Env,
    | "OCEL_REFRESH_QUEUE"
    | "OCEL_REVALIDATE_QUEUE_URL"
    | "OCEL_EDGE_ACCESS_KEY_ID"
    | "OCEL_EDGE_SECRET_KEY"
  >,
  timeoutMs?: number,
): RevalidationSender | undefined {
  if (env.OCEL_REFRESH_QUEUE) return cloudflareQueueSender(env.OCEL_REFRESH_QUEUE, timeoutMs);
  const queueUrl = env.OCEL_REVALIDATE_QUEUE_URL;
  if (!queueUrl) return undefined;
  const send = sqsFetch(env.OCEL_EDGE_ACCESS_KEY_ID, env.OCEL_EDGE_SECRET_KEY, sqsRegion(queueUrl));
  return send && queueSender(queueUrl, send, timeoutMs);
}

export async function enqueued(
  send: RevalidationSender | undefined,
  route: RevalidationRoute | undefined,
  lastModified: number,
): Promise<boolean> {
  if (!send || !route) return false;
  try {
    return await send(revalidationMessage(route, lastModified), route.origin);
  } catch {
    return false;
  }
}
