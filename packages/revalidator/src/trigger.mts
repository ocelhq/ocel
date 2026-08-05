import { AwsClient } from "aws4fetch";

import { triggerTimeoutMs } from "./limits.mjs";
import type { Outcome } from "./log.mjs";
import type { RevalidationMessage } from "./message.mjs";
import type { Target } from "./origin.mjs";

// One message, one signed HEAD at the origin.
//
// HEAD runs the whole render and cache-write pipeline and skips only the body
// write, so the trigger costs a render and no transfer. What comes back is read
// framework-blind: the edge declared the receipt it expects when it enqueued
// the message, and this evaluates that declaration and nothing else.
//
// The target is a `Target`, which only origin.mts produces. Nothing here can be
// pointed at a host the deploy did not record, because there is no other way to
// hand this function a URL.
export interface TriggerDeps {
  fetch: typeof fetch;
  credentials: { accessKeyId: string; secretAccessKey: string; sessionToken?: string };
  timeoutMs?: number;
}

export async function trigger(
  deps: TriggerDeps,
  target: Target,
  message: RevalidationMessage,
): Promise<Outcome> {
  const client = new AwsClient({ ...deps.credentials, service: "lambda", region: target.region });
  // The message's own headers are signed along with `host`, so what AWS
  // authorizes is exactly the request that gets sent. Nothing sits inside the
  // TLS session to rewrite them, so signing them costs nothing and narrows what
  // a captured signature could be replayed with.
  const signed = await client.sign(target.url, { method: "HEAD", headers: message.headers });

  const signal = AbortSignal.timeout(deps.timeoutMs ?? triggerTimeoutMs);
  let response: Response;
  try {
    response = await deps.fetch(target.url, { method: "HEAD", headers: signed.headers, signal });
  } catch {
    return { event: "RevalidateFailed", reason: signal.aborted ? "timeout" : "fetch-failed" };
  }

  if (!response.ok) return { event: "RevalidateFailed", reason: "status-not-ok", status: response.status };
  if (message.expect === null) return { event: "RevalidateOk" };

  const got = response.headers.get(message.expect.header);
  // A route that went dynamic since it was enqueued answers ok without the
  // receipt. Redelivering the message cannot change that, so this is a success
  // that says so rather than a failure that loops until the DLQ.
  if (got !== message.expect.value) {
    return { event: "RevalidateExpectMiss", expected: message.expect.value, got };
  }
  return { event: "RevalidateOk" };
}
