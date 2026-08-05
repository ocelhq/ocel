import { AwsClient } from "aws4fetch";

import type { Outcome } from "./log.mjs";
import type { RevalidationMessage } from "./message.mjs";

// One message, one signed HEAD at the origin.
//
// HEAD runs the whole render and cache-write pipeline and skips only the body
// write, so the trigger costs a render and no transfer. What comes back is read
// framework-blind: the edge declared the receipt it expects when it enqueued
// the message, and this evaluates that declaration and nothing else.
export interface TriggerDeps {
  fetch: typeof fetch;
  credentials: { accessKeyId: string; secretAccessKey: string; sessionToken?: string };
  timeoutMs?: number;
}

// One render's budget. Ten of these back to back is the batch's worst case,
// which is what the function timeout and the queue's visibility timeout are
// sized from (see README).
export const triggerTimeoutMs = 10_000;

// The region a Function URL host names: `<id>.lambda-url.<region>.on.aws`.
// aws4fetch guesses region and service from `*.amazonaws.com` hosts only, and a
// wrong guess is an opaque 403, so it is read here and passed explicitly.
function regionOf(host: string): string | undefined {
  const labels = host.split(".");
  const i = labels.indexOf("lambda-url");
  return i < 0 ? undefined : labels[i + 1];
}

// The headers a signature produces. They are copied onto the real request so
// that what AWS authorizes is exactly `host` plus these: the message's own
// headers ride unsigned, which keeps a header rewritten in transit from
// invalidating the signature.
const SIGV4_HEADERS = ["authorization", "x-amz-date", "x-amz-content-sha256", "x-amz-security-token"];

export async function trigger(deps: TriggerDeps, message: RevalidationMessage): Promise<Outcome> {
  const region = regionOf(new URL(message.url).host);
  if (region === undefined) return { event: "RevalidateFailed", reason: "unsignable-host" };

  const client = new AwsClient({ ...deps.credentials, service: "lambda", region });
  const signed = await client.sign(message.url, { method: "HEAD" });

  const headers = new Headers(message.headers);
  for (const name of SIGV4_HEADERS) {
    const value = signed.headers.get(name);
    if (value !== null) headers.set(name, value);
  }

  const signal = AbortSignal.timeout(deps.timeoutMs ?? triggerTimeoutMs);
  let response: Response;
  try {
    response = await deps.fetch(message.url, { method: "HEAD", headers, signal });
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
