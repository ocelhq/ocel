import { CloudFrontClient, CreateInvalidationCommand } from "@aws-sdk/client-cloudfront";
import { DynamoDBClient, GetItemCommand } from "@aws-sdk/client-dynamodb";
import { GetParameterCommand, SSMClient } from "@aws-sdk/client-ssm";

import { config } from "./config.mjs";
import { invalidateAll } from "./invalidate.mjs";
import { raisesOf, type StreamRecord } from "./records.mjs";

const attempts = 5;

const cloudfront = new CloudFrontClient({ maxAttempts: attempts });
const dynamo = new DynamoDBClient({ maxAttempts: attempts });
const ssm = new SSMClient({ maxAttempts: attempts });

let seed: Promise<string> | undefined;

function readSeed(param: string): Promise<string> {
  return (seed ??= ssm
    .send(new GetParameterCommand({ Name: param, WithDecryption: true }))
    .then((out) => {
      const value = out.Parameter?.Value;
      if (value === undefined || value === "") throw new Error(`SSM parameter ${param} is empty`);
      return value;
    })
    .catch((error) => {
      seed = undefined;
      throw error;
    }));
}

export interface BatchResponse {
  batchItemFailures: { itemIdentifier: string }[];
}

export const handler = async (event: { Records?: StreamRecord[] }): Promise<BatchResponse> => {
  const raises = raisesOf(event.Records ?? []);
  if (raises.size === 0) return { batchItemFailures: [] };

  const { table, bootstrapTier, seedParam } = config(process.env);
  const failed = await invalidateAll(
    {
      cloudfront,
      dynamo,
      commands: { CreateInvalidationCommand, GetItemCommand },
      table,
      bootstrapTier,
      cloudflare:
        seedParam === undefined ? undefined : { fetch, readSeed: () => readSeed(seedParam) },
    },
    raises,
  );
  return { batchItemFailures: failed.map((itemIdentifier) => ({ itemIdentifier })) };
};
