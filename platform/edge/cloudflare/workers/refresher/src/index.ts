import type { Env } from "./env";
import { parseRefresh, type RefreshOutcome, trigger } from "./refresh";

const baseDelaySeconds = 15;
const maxDelaySeconds = 300;

function computeRetryDelay(attempts: number): number {
  return Math.min(maxDelaySeconds, baseDelaySeconds * 2 ** (attempts - 1));
}

function log(
  outcome: RefreshOutcome,
  item: Message<unknown>,
  fields: Record<string, unknown>,
): void {
  const { settle: _settle, ...rest } = outcome;
  console.log(JSON.stringify({ ...rest, messageId: item.id, attempts: item.attempts, ...fields }));
}

async function settle(item: Message<unknown>, env: Env): Promise<void> {
  const parsed = parseRefresh(item.body);
  const outcome = parsed.ok
    ? await trigger(parsed.refresh, env.OCEL_ORIGIN_CLIENT_CERTIFICATE)
    : parsed.outcome;
  const { isrPrefix, routePath, lastModified, enqueuedAt } = parsed.ok
    ? parsed.refresh.message
    : {};
  log(outcome, item, { isrPrefix, routePath, lastModified, enqueuedAt });
  if (outcome.settle === "ack") item.ack();
  else item.retry({ delaySeconds: computeRetryDelay(item.attempts) });
}

async function queue(batch: MessageBatch<unknown>, env: Env): Promise<void> {
  for (const item of batch.messages) {
    try {
      await settle(item, env);
    } catch {
      console.log(
        JSON.stringify({
          event: "RevalidateFailed",
          reason: "handler-error",
          messageId: item.id,
          attempts: item.attempts,
        }),
      );
      item.retry({ delaySeconds: computeRetryDelay(item.attempts) });
    }
  }
}

export default { queue } satisfies ExportedHandler<Env>;
