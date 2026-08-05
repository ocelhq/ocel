import { parseMessage, type RevalidationMessage } from "../src/message.mjs";
import { resolve, type Target } from "../src/origin.mjs";

// One deploy, spelled once: the substrate's asset bucket, the record the deploy
// wrote under this build's isrPrefix, and the Function URL that record names.
// Tests that only care about one of these should still take all three from
// here, so a change to the record's shape breaks in one place.
export const bucket = "ocel-assets-1a2b3c";
export const region = "us-east-1";
export const isrPrefix = "prod/proj/web/BID";
export const routeId = "/";
export const host = "abc123.lambda-url.us-east-1.on.aws";
export const originUrl = `https://${host}/`;
export const recordUrl = `https://${bucket}.s3.${region}.amazonaws.com/${isrPrefix}/origin.json`;

export function originDocument(functionUrls: Record<string, unknown> = { [routeId]: originUrl }): string {
  return JSON.stringify({ v: 1, functionUrls });
}

export function body(overrides: Record<string, unknown> = {}): string {
  return JSON.stringify({
    v: 1,
    headers: { "x-prerender-revalidate": "s3cr3t-preview-mode-id", "x-forwarded-host": "example.com" },
    expect: { header: "x-nextjs-cache", value: "REVALIDATED" },
    isrPrefix,
    routeId,
    routePath: "/blog/post",
    lastModified: 1_700_000_000_000,
    enqueuedAt: 1_700_000_000_500,
    ...overrides,
  });
}

export const credentials = { accessKeyId: "AKIAEXAMPLE", secretAccessKey: "shhh", sessionToken: "session" };

// A substrate that serves the deploy record and nothing else.
export function recordFetch(document: string = originDocument()): typeof fetch {
  return (async (input: string | Request) => {
    const url = typeof input === "string" ? input : input.url;
    if (url !== recordUrl) throw new Error(`unexpected fetch: ${url}`);
    return new Response(document, { status: 200 });
  }) as typeof fetch;
}

// A Target, obtained the only way anything can obtain one: by resolving a
// parsed message against a deploy record. A test that wants to trigger has to
// go through the resolver, which is the property the seam exists for.
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
