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

export type RevalidationRoute = Omit<
  RevalidationMessage,
  "v" | "lastModified" | "enqueuedAt"
>;

export function revalidationMessage(
  route: RevalidationRoute,
  lastModified: number,
  enqueuedAt: number = Date.now(),
): RevalidationMessage {
  return { v: 1, ...route, lastModified, enqueuedAt };
}

export interface RevalidationIds {
  MessageGroupId: string;
  MessageDeduplicationId: string;
}

const maxIdLength = 128;

export const revalidationRetryWindowMs = 30_000;

function retryWindow(enqueuedAt: number): number {
  return Math.floor(enqueuedAt / revalidationRetryWindowMs);
}

async function sha256Hex(value: string): Promise<string> {
  const digest = await crypto.subtle.digest(
    "SHA-256",
    new TextEncoder().encode(value),
  );
  return [...new Uint8Array(digest)]
    .map((byte) => byte.toString(16).padStart(2, "0"))
    .join("");
}

export async function revalidationIds(
  message: RevalidationMessage,
): Promise<RevalidationIds> {
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

export type RevalidationSender = (
  message: RevalidationMessage,
) => Promise<boolean>;

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

export function revalidationSender(
  queueUrl: string | undefined,
  accessKeyId: string | undefined,
  secretAccessKey: string | undefined,
  timeoutMs?: number,
): RevalidationSender | undefined {
  if (!queueUrl) return undefined;
  const send = sqsFetch(accessKeyId, secretAccessKey, sqsRegion(queueUrl));
  return send && queueSender(queueUrl, send, timeoutMs);
}

export async function enqueued(
  send: RevalidationSender | undefined,
  route: RevalidationRoute | undefined,
  lastModified: number,
): Promise<boolean> {
  if (!send || !route) return false;
  try {
    return await send(revalidationMessage(route, lastModified));
  } catch {
    return false;
  }
}
