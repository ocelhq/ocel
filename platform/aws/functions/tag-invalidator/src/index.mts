import { CloudFrontClient, CreateInvalidationCommand } from "@aws-sdk/client-cloudfront";
import { DynamoDBClient, GetItemCommand } from "@aws-sdk/client-dynamodb";

import { config } from "./config.mjs";
import { invalidateAll } from "./invalidate.mjs";
import { raisesOf, type StreamRecord } from "./records.mjs";

const attempts = 5;

const cloudfront = new CloudFrontClient({ maxAttempts: attempts });
const dynamo = new DynamoDBClient({ maxAttempts: attempts });

export interface BatchResponse {
  batchItemFailures: { itemIdentifier: string }[];
}

export const handler = async (event: { Records?: StreamRecord[] }): Promise<BatchResponse> => {
  const raises = raisesOf(event.Records ?? []);
  if (raises.size === 0) return { batchItemFailures: [] };

  const { table, bootstrapClass } = config(process.env);
  const failed = await invalidateAll(
    {
      cloudfront,
      dynamo,
      commands: { CreateInvalidationCommand, GetItemCommand },
      table,
      bootstrapClass,
    },
    raises,
  );
  return { batchItemFailures: failed.map((itemIdentifier) => ({ itemIdentifier })) };
};
