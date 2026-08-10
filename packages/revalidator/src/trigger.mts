import { AwsClient } from "aws4fetch";

import { triggerTimeoutMs } from "./limits.mjs";
import type { Outcome } from "./log.mjs";
import type { RevalidationMessage } from "./message.mjs";
import type { Target } from "./origin.mjs";

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
  if (got !== message.expect.value) {
    return { event: "RevalidateExpectMiss", expected: message.expect.value, got };
  }
  return { event: "RevalidateOk" };
}
