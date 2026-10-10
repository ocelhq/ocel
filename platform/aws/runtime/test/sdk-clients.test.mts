import { once } from "node:events";
import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { GetItemCommand } from "@aws-sdk/client-dynamodb";
import { GetObjectCommand } from "@aws-sdk/client-s3";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { newDynamoDBClient, newS3Client } from "../src/next/sdk-clients.mjs";

let server: Server;

beforeEach(async () => {
  server = createServer(() => {});
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const endpoint = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
  vi.stubEnv("AWS_ENDPOINT_URL", endpoint);
  vi.stubEnv("AWS_MAX_ATTEMPTS", "1");
  vi.stubEnv("AWS_REGION", "us-east-1");
  vi.stubEnv("AWS_ACCESS_KEY_ID", "test");
  vi.stubEnv("AWS_SECRET_ACCESS_KEY", "test");
  vi.stubEnv("AWS_SESSION_TOKEN", "");
});

afterEach(async () => {
  vi.unstubAllEnvs();
  server.closeAllConnections();
  server.close();
});

async function elapsedUntilSettled(call: Promise<unknown>): Promise<[number, unknown]> {
  const started = Date.now();
  const outcome = await call.then(
    () => undefined,
    (err) => err,
  );
  return [Date.now() - started, outcome];
}

test("an S3 call to a backend that never answers fails within the request timeout", async () => {
  const [elapsed, err] = await elapsedUntilSettled(
    newS3Client().send(new GetObjectCommand({ Bucket: "assets", Key: "snapshot.json" })),
  );

  expect(err).toMatchObject({ name: "TimeoutError" });
  expect(elapsed).toBeLessThan(8_000);
}, 15_000);

test("a DynamoDB call to a backend that never answers fails within the request timeout", async () => {
  const [elapsed, err] = await elapsedUntilSettled(
    newDynamoDBClient().send(new GetItemCommand({ TableName: "state", Key: { pk: { S: "k" } } })),
  );

  expect(err).toMatchObject({ name: "TimeoutError" });
  expect(elapsed).toBeLessThan(8_000);
}, 15_000);
