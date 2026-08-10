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
    for (const name of SIGV4_HEADERS) {
      const value = signed.headers.get(name);
      if (value) headers.set(name, value);
    }

    return fetch(
      new Request(request.url, {
        method: request.method,
        headers,
        body,
        redirect: "manual",
      }),
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
  return ((input, init) =>
    client.fetch(input as RequestInfo, init)) as typeof fetch;
}

export type AwsService = "s3" | "dynamodb";

export type AwsServiceFetch = (
  service: AwsService,
  url: string,
  init?: RequestInit,
) => Promise<Response>;

const serviceRetries = 1;

export function awsServiceFetch(
  accessKeyId: string | undefined,
  secretAccessKey: string | undefined,
  region: string | undefined,
): AwsServiceFetch | undefined {
  if (!accessKeyId || !secretAccessKey || !region) return undefined;
  const options = { accessKeyId, secretAccessKey, region, retries: serviceRetries };
  const clients: Record<AwsService, AwsClient> = {
    s3: new AwsClient({ ...options, service: "s3" }),
    dynamodb: new AwsClient({ ...options, service: "dynamodb" }),
  };
  return (service, url, init) => clients[service].fetch(url, init);
}
