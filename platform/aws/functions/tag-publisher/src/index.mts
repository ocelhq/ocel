import { GetObjectCommand, PutObjectCommand, S3Client } from "@aws-sdk/client-s3";
import { SSMClient } from "@aws-sdk/client-ssm";

import { config } from "./config.mjs";
import { publishAll } from "./publish.mjs";
import { raisesOf, type StreamRecord } from "./records.mjs";

const s3 = new S3Client({});
const ssm = new SSMClient({});

export interface BatchResponse {
  batchItemFailures: { itemIdentifier: string }[];
}

export const handler = async (event: { Records?: StreamRecord[] }): Promise<BatchResponse> => {
  const raises = raisesOf(event.Records ?? []);
  if (raises.size === 0) return { batchItemFailures: [] };

  const { assetBucket, endpoint, seed } = await config(ssm);
  const failed = await publishAll(
    { s3, commands: { GetObjectCommand, PutObjectCommand }, fetch, assetBucket, endpoint, seed },
    raises,
    Date.now(),
  );
  return { batchItemFailures: failed.map((itemIdentifier) => ({ itemIdentifier })) };
};
