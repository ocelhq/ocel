import { UpdateItemCommand } from "@aws-sdk/client-dynamodb";
import type { TagRecordWriter } from "@framework/node-runtime/tag-records";
import type { PurgeEntry } from "@platform/edge-contract/purge";
import { newDynamoDBClient } from "../cache/sdk-clients.mjs";
import { isGuardRejection, tagRecordUpdate } from "../cache/tag-index.mjs";

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
    const writes = await Promise.allSettled(
      entries.map(async (entry) => {
        try {
          await database.send(
            new UpdateItemCommand(
              tagRecordUpdate(table, namespace, entry.tag, buildTagRecord(entry)),
            ),
          );
        } catch (error) {
          if (!isGuardRejection(error)) throw error;
        }
      }),
    );
    const failed = writes.find((write) => write.status === "rejected");
    if (failed) throw failed.reason;
  };
}
