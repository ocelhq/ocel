import { DynamoDBClient } from "@aws-sdk/client-dynamodb";
import { S3Client } from "@aws-sdk/client-s3";

const requestHandler = {
  connectionTimeout: 2_000,
  requestTimeout: 5_000,
  throwOnRequestTimeout: true,
  socketTimeout: 5_000,
};

export function newS3Client(): S3Client {
  return new S3Client({ requestHandler });
}

export function newDynamoDBClient(): DynamoDBClient {
  return new DynamoDBClient({ requestHandler });
}
