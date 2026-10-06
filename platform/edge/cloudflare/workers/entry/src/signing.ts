import { carryClientAuthorization } from "@platform/edge-contract/client-authorization";
import { dropEmptyBodySentinel } from "@platform/edge-contract/empty-body";
import { AwsClient } from "aws4fetch";

export function lambdaRegion(host: string): string | undefined {
  const labels = host.split(".");
  const i = labels.indexOf("lambda-url");
  if (i < 0 || i + 1 >= labels.length) return undefined;
  return labels[i + 1];
}

const SIGV4_HEADERS = [
  "authorization",
  "x-amz-date",
  "x-amz-content-sha256",
  "x-amz-security-token",
];

const REMAPPED_PREFIX = "x-amzn-remapped-";

const UNRESTORABLE = new Set([
  "connection",
  "keep-alive",
  "proxy-connection",
  "transfer-encoding",
  "te",
  "trailer",
  "upgrade",
  "content-length",
]);

function restoreRemappedHeaders(response: Response): Response {
  const remapped = [...response.headers.keys()].filter((name) => name.startsWith(REMAPPED_PREFIX));
  if (remapped.length === 0) return response;
  const restored = new Response(response.body, response);
  for (const name of remapped) {
    const value = restored.headers.get(name);
    restored.headers.delete(name);
    const original = name.slice(REMAPPED_PREFIX.length);
    if (value === null || UNRESTORABLE.has(original) || restored.headers.has(original)) continue;
    restored.headers.set(original, value);
  }
  return restored;
}

export function edgeOriginFetch(
  accessKeyId: string | undefined,
  secretAccessKey: string | undefined,
): typeof fetch | undefined {
  if (!accessKeyId || !secretAccessKey) return undefined;
  const client = new AwsClient({ accessKeyId, secretAccessKey, service: "lambda" });
  return (async (input, init) => {
    const request = new Request(input as RequestInfo, init);
    const host = new URL(request.url).host;
    const region = lambdaRegion(host);
    if (!region) {
      throw new Error(`cannot sign request to non-Function-URL host: ${host}`);
    }

    const hasBody = request.method !== "GET" && request.method !== "HEAD";
    const body = hasBody ? await request.arrayBuffer() : undefined;

    const signed = await client.sign(request.url, {
      method: request.method,
      body,
      aws: { region },
    });

    const headers = new Headers(request.headers);
    carryClientAuthorization(headers);
    for (const name of SIGV4_HEADERS) {
      const value = signed.headers.get(name);
      if (value) headers.set(name, value);
    }

    return dropEmptyBodySentinel(
      restoreRemappedHeaders(
        await fetch(
          new Request(request.url, {
            method: request.method,
            headers,
            body,
            redirect: "manual",
          }),
        ),
      ),
    );
  }) as typeof fetch;
}

export function sqsRegion(queueUrl: string): string | undefined {
  let host: string;
  try {
    host = new URL(queueUrl).host;
  } catch {
    return undefined;
  }
  const labels = host.split(".");
  return labels[0] === "sqs" && labels.length > 2 ? labels[1] : undefined;
}

export function sqsFetch(
  accessKeyId: string | undefined,
  secretAccessKey: string | undefined,
  region: string | undefined,
): typeof fetch | undefined {
  if (!accessKeyId || !secretAccessKey || !region) return undefined;
  const client = new AwsClient({
    accessKeyId,
    secretAccessKey,
    region,
    service: "sqs",
    retries: 0,
  });
  return ((input, init) => client.fetch(input as RequestInfo, init)) as typeof fetch;
}

export type DynamoDbFetch = (url: string, init?: RequestInit) => Promise<Response>;

const dynamoDbRetries = 1;

export function dynamoDbFetch(
  accessKeyId: string | undefined,
  secretAccessKey: string | undefined,
  region: string | undefined,
): DynamoDbFetch | undefined {
  if (!accessKeyId || !secretAccessKey || !region) return undefined;
  const client = new AwsClient({
    accessKeyId,
    secretAccessKey,
    region,
    service: "dynamodb",
    retries: dynamoDbRetries,
  });
  return (url, init) => client.fetch(url, init);
}
