// The send side of queue-deduplicated revalidation (design §4): an admitted
// background refresh hands the render to an account-level SQS FIFO queue whose
// consumer performs it once, instead of rendering it here. L1's jitter bounds
// how many colos ask; only the queue bounds how many renders result.
//
// Every failure here answers false, and false means the caller renders through
// originBlocking exactly as it did before the queue existed. There is no retry:
// the fallback IS the retry.
import { sqsFetch, sqsRegion } from "./signing";

// What the consumer parses (packages/revalidator/src/message.mts). It names no
// host: the consumer resolves the origin from the record the deploy itself
// wrote, keyed by isrPrefix and routeId, so a stolen edge credential can at
// worst name a route this deploy does not have.
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

// The route-constant half of the message: everything the edge knows before any
// staleness verdict exists. The verdict supplies the rest.
export type RevalidationRoute = Omit<
  RevalidationMessage,
  "v" | "lastModified" | "enqueuedAt"
>;

// The framework contract, declared by the edge because the edge is the only
// layer that knows the framework (design §8). Next stamps this on a response it
// actually regenerated; the consumer only compares what it is handed.
export const NEXT_RENDER_RECEIPT = {
  header: "x-nextjs-cache",
  value: "REVALIDATED",
};

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

// SQS's own limit on both ids.
const maxIdLength = 128;

async function sha256Hex(value: string): Promise<string> {
  const digest = await crypto.subtle.digest(
    "SHA-256",
    new TextEncoder().encode(value),
  );
  return [...new Uint8Array(digest)]
    .map((byte) => byte.toString(16).padStart(2, "0"))
    .join("");
}

// The dedup id is one render per route per entry generation: every colo that
// finds the same stale entry derives the same id, and SQS collapses them. The
// group id serializes a route's renders against each other without serializing
// it against any other route.
//
// A route long enough to overflow the group id keeps a readable head and a hash
// of the whole thing, so two long routes sharing a head are still two groups.
// Both halves are pure and exported so they are asserted directly.
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
      `${group}:${message.lastModified}`,
    ),
  };
}

export type RevalidationSender = (
  message: RevalidationMessage,
) => Promise<boolean>;

// A send that outlives this is worth less than the render it was deferring: the
// admission is holding the colo's claim while it waits, and originBlocking is
// one hop away.
export const enqueueTimeoutMs = 1_000;

export function queueSender(
  queueUrl: string,
  send: typeof fetch,
  timeoutMs: number = enqueueTimeoutMs,
): RevalidationSender {
  return async (message) => {
    try {
      const ids = await revalidationIds(message);
      // The AWS query protocol, not JSON: JSON is an SDK-side upgrade and we carry
      // no SDK, and AWS commits to supporting query (design §1 fact 10, §12).
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
      // An empty queue is the healthy state, so a queue that refuses every send
      // looks exactly like a queue nobody is filling. The status is what tells
      // a misconfigured region or policy apart from a queue with no work in it;
      // the record itself is never logged (design amendment D) because it
      // carries the app's bypass token.
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

// Built only where the substrate binds a queue and the credentials to send to
// it. Absent — an older bootstrap, or one whose provider build pins no
// revalidator artifact to drain the queue — every admitted refresh renders
// through the origin exactly as it did before this path existed.
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

// The enqueue as an admission site takes it: never throws, and answers false
// for every reason there is not to have deferred this render — no queue bound,
// no route to name, or a queue that would not take it. The catch is dead
// against queueSender, which answers false itself and logs why it did; it is
// here so a sender this site did not build cannot take an admission down with
// it.
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
