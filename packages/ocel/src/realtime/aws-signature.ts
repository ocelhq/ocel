import { createHash, createHmac } from "node:crypto";
import { readFile } from "node:fs/promises";

/** The AWS credentials a request is signed with. */
export interface AwsCredentials {
  accessKeyId: string;
  secretAccessKey: string;
  sessionToken?: string;
}

const containerCredentialsOrigin = "http://169.254.170.2";
const refreshBeforeExpiryMilliseconds = 5 * 60_000;

let heldContainerCredentials: { credentials: AwsCredentials; expiresAt: number } | undefined;

/** Drops the container credentials held from an earlier read, so the next read fetches them. */
export function forgetContainerCredentials(): void {
  heldContainerCredentials = undefined;
}

async function readContainerCredentials(endpoint: string): Promise<AwsCredentials> {
  if (
    heldContainerCredentials &&
    heldContainerCredentials.expiresAt - Date.now() > refreshBeforeExpiryMilliseconds
  ) {
    return heldContainerCredentials.credentials;
  }
  const tokenFile = process.env.AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE;
  const token = tokenFile
    ? (await readFile(tokenFile, "utf8")).trim()
    : process.env.AWS_CONTAINER_AUTHORIZATION_TOKEN;
  const response = await fetch(endpoint, token ? { headers: { authorization: token } } : {});
  if (!response.ok) {
    throw new Error(`the container's AWS credentials endpoint answered status ${response.status}`);
  }
  const answered = (await response.json()) as {
    AccessKeyId?: string;
    SecretAccessKey?: string;
    Token?: string;
    Expiration?: string;
  };
  if (!answered.AccessKeyId || !answered.SecretAccessKey) {
    throw new Error("the container's credentials endpoint answered no AWS credentials");
  }
  const credentials: AwsCredentials = {
    accessKeyId: answered.AccessKeyId,
    secretAccessKey: answered.SecretAccessKey,
    ...(answered.Token ? { sessionToken: answered.Token } : {}),
  };
  heldContainerCredentials = {
    credentials,
    expiresAt: answered.Expiration ? Date.parse(answered.Expiration) : 0,
  };
  return credentials;
}

/**
 * Reads the credentials AWS hands the app: the environment's keys on Lambda, or the
 * container credentials endpoint on ECS. Throws when neither is there.
 */
export async function readAwsCredentials(): Promise<AwsCredentials> {
  const accessKeyId = process.env.AWS_ACCESS_KEY_ID;
  const secretAccessKey = process.env.AWS_SECRET_ACCESS_KEY;
  if (accessKeyId && secretAccessKey) {
    const sessionToken = process.env.AWS_SESSION_TOKEN;
    return { accessKeyId, secretAccessKey, ...(sessionToken ? { sessionToken } : {}) };
  }
  const relative = process.env.AWS_CONTAINER_CREDENTIALS_RELATIVE_URI;
  const endpoint = relative
    ? containerCredentialsOrigin + relative
    : process.env.AWS_CONTAINER_CREDENTIALS_FULL_URI;
  if (!endpoint) {
    throw new Error(
      "no AWS credentials to sign an AppSync publish with: set AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY, or run where AWS_CONTAINER_CREDENTIALS_RELATIVE_URI is delivered",
    );
  }
  return readContainerCredentials(endpoint);
}

/** Reads the region an AppSync API's HTTP host is in, falling back to `AWS_REGION`. */
export function findAppSyncRegion(host: string): string {
  const match = /\.appsync-api\.([a-z0-9-]+)\./.exec(host);
  return match?.[1] ?? process.env.AWS_REGION ?? "";
}

function hashSha256(data: string): string {
  return createHash("sha256").update(data).digest("hex");
}

function signHmac(key: string | Buffer, data: string): Buffer {
  return createHmac("sha256", key).update(data).digest();
}

/**
 * Signs a `POST /event` of `body` to the AppSync API at `host` with SigV4, answering the
 * headers the request is sent with.
 */
export function signAppSyncPublish(
  host: string,
  body: string,
  credentials: AwsCredentials,
  region: string,
  at: Date,
): Record<string, string> {
  const stamp = at
    .toISOString()
    .replace(/[-:]/g, "")
    .replace(/\.\d{3}/, "");
  const day = stamp.slice(0, 8);
  const signed: [string, string][] = [
    ["content-type", "application/json"],
    ["host", host],
    ["x-amz-date", stamp],
  ];
  if (credentials.sessionToken) signed.push(["x-amz-security-token", credentials.sessionToken]);
  const signedHeaders = signed.map(([name]) => name).join(";");
  const canonicalRequest = [
    "POST",
    "/event",
    "",
    signed.map(([name, value]) => `${name}:${value}\n`).join(""),
    signedHeaders,
    hashSha256(body),
  ].join("\n");
  const scope = `${day}/${region}/appsync/aws4_request`;
  const stringToSign = ["AWS4-HMAC-SHA256", stamp, scope, hashSha256(canonicalRequest)].join("\n");
  let key = signHmac(`AWS4${credentials.secretAccessKey}`, day);
  for (const part of [region, "appsync", "aws4_request"]) key = signHmac(key, part);
  const signature = signHmac(key, stringToSign).toString("hex");
  return {
    "content-type": "application/json",
    "x-amz-date": stamp,
    ...(credentials.sessionToken ? { "x-amz-security-token": credentials.sessionToken } : {}),
    authorization: `AWS4-HMAC-SHA256 Credential=${credentials.accessKeyId}/${scope}, SignedHeaders=${signedHeaders}, Signature=${signature}`,
  };
}
