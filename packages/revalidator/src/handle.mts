import { context, report, type Outcome } from "./log.mjs";
import { parseMessage } from "./message.mjs";
import { resolve, type OriginDeps } from "./origin.mjs";
import { trigger, type TriggerDeps } from "./trigger.mjs";

// A batch of SQS FIFO records, in the shape the event source mapping sends and
// no wider — the wire format is interpreted in exactly one place.
export interface SqsRecord {
  messageId: string;
  body: string;
  attributes?: { MessageGroupId?: string };
}

// ReportBatchItemFailures (declared on the event source mapping in
// cloud/aws/bootstrap): only the reported records are redelivered.
export interface BatchResponse {
  batchItemFailures: { itemIdentifier: string }[];
}

export interface HandlerDeps extends TriggerDeps, OriginDeps {}

async function once(deps: HandlerDeps, record: SqsRecord): Promise<Outcome> {
  const parsed = parseMessage(record.body);
  if (!parsed.ok) {
    report(context(record.messageId, null), { event: "RevalidateFailed", reason: parsed.reason });
    return { event: "RevalidateFailed", reason: parsed.reason };
  }

  const resolution = await resolve(deps, parsed.message);
  if (!resolution.ok) {
    const outcome: Outcome = { event: "RevalidateFailed", reason: resolution.reason };
    report(context(record.messageId, parsed.message), outcome);
    return outcome;
  }

  const outcome = await trigger(deps, resolution.target, parsed.message);
  report(context(record.messageId, parsed.message), outcome);
  return outcome;
}

// The FIFO contract: same-group messages are ordered, and a group that fails is
// redelivered from the failure onward. So the first failure in a group ends
// that group for this batch — its later records are reported unprocessed rather
// than run out of order — while other groups carry on.
//
// Nothing is allowed to throw out of here: a thrown handler fails the whole
// batch, including groups that succeeded.
export async function handle(deps: HandlerDeps, event: { Records?: SqsRecord[] }): Promise<BatchResponse> {
  const batchItemFailures: { itemIdentifier: string }[] = [];
  const stopped = new Set<string>();

  for (const record of event.Records ?? []) {
    const group = record.attributes?.MessageGroupId ?? record.messageId;
    if (stopped.has(group)) {
      batchItemFailures.push({ itemIdentifier: record.messageId });
      continue;
    }

    let outcome: Outcome;
    try {
      outcome = await once(deps, record);
    } catch {
      outcome = { event: "RevalidateFailed", reason: "handler-error" };
      report(context(record.messageId, null), outcome);
    }

    if (outcome.event === "RevalidateFailed") {
      stopped.add(group);
      batchItemFailures.push({ itemIdentifier: record.messageId });
    }
  }

  return { batchItemFailures };
}
