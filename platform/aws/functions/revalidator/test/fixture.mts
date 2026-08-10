import { parseMessage, type RevalidationMessage } from "@platform/edge-contract/revalidation";
import { body, isrPrefix, routeId } from "@platform/edge-contract/revalidation/sample";
import { resolve, type Target } from "../src/origin.mjs";

export { body, isrPrefix, routeId };

export const bucket = "ocel-assets-1a2b3c";
export const region = "us-east-1";
export const host = "abc123.lambda-url.us-east-1.on.aws";
export const originUrl = `https://${host}/`;
export const recordUrl = `https://${bucket}.s3.${region}.amazonaws.com/${isrPrefix}/origin.json`;

export function originDocument(functionUrls: Record<string, unknown> = { [routeId]: originUrl }): string {
  return JSON.stringify({ v: 1, functionUrls });
}

export const credentials = { accessKeyId: "AKIAEXAMPLE", secretAccessKey: "shhh", sessionToken: "session" };

export function recordFetch(document: string = originDocument()): typeof fetch {
  return (async (input: string | Request) => {
    const url = typeof input === "string" ? input : input.url;
    if (url !== recordUrl) throw new Error(`unexpected fetch: ${url}`);
    return new Response(document, { status: 200 });
  }) as typeof fetch;
}

export async function resolved(
  overrides: Record<string, unknown> = {},
): Promise<{ target: Target; message: RevalidationMessage }> {
  const parsed = parseMessage(body(overrides));
  if (!parsed.ok) throw new Error(`fixture message rejected: ${parsed.reason}`);
  const resolution = await resolve(
    { fetch: recordFetch(), credentials, bucket, region, origins: new Map() },
    parsed.message,
  );
  if (!resolution.ok) throw new Error(`fixture message unresolved: ${resolution.reason}`);
  return { target: resolution.target, message: parsed.message };
}
