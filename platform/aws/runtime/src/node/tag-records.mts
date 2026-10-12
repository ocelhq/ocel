import { UpdateItemCommand } from "@aws-sdk/client-dynamodb";
import type { TagRecordWriter } from "@framework/node-runtime/tag-records";
import type { PurgeEntry } from "@platform/edge-contract/purge";
import { newDynamoDBClient } from "../cache/sdk-clients.mjs";
import { isGuardRejection, tagRecordUpdate } from "../cache/tag-index.mjs";

const writesInFlight = 8;

function buildTagRecord({ mode, raisedAt }: PurgeEntry) {
  return mode === "purge"
    ? { expired: raisedAt, writtenAt: raisedAt }
    : { stale: raisedAt, writtenAt: raisedAt };
}

export function newTagRecordWriter(env: NodeJS.ProcessEnv): TagRecordWriter | undefined {
  const table = env.OCEL_STATE_TABLE;
  const namespace = env.OCEL_ISR_TAG_NAMESPACE;
  if (!table || !namespace) return undefined;
  let client: ReturnType<typeof newDynamoDBClient> | undefined;
  return async (entries) => {
    const database = (client ??= newDynamoDBClient());
    const failures: unknown[] = [];
    let next = 0;
    const writeEach = async () => {
      while (next < entries.length) {
        const entry = entries[next++]!;
        try {
          await database.send(
            new UpdateItemCommand(
              tagRecordUpdate(table, namespace, entry.tag, buildTagRecord(entry)),
            ),
          );
        } catch (error) {
          if (!isGuardRejection(error)) failures.push(error);
        }
      }
    };
    await Promise.all(Array.from({ length: Math.min(writesInFlight, entries.length) }, writeEach));
    if (failures.length > 0) throw failures[0];
  };
}
