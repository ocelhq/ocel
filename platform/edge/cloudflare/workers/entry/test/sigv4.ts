const encoder = new TextEncoder();

async function hmac(key: ArrayBuffer | string, data: string): Promise<ArrayBuffer> {
  const imported = await crypto.subtle.importKey(
    "raw",
    typeof key === "string" ? encoder.encode(key) : key,
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  return crypto.subtle.sign("HMAC", imported, encoder.encode(data));
}

function hex(buffer: ArrayBuffer): string {
  return [...new Uint8Array(buffer)].map((byte) => byte.toString(16).padStart(2, "0")).join("");
}

async function sha256Hex(data: string): Promise<string> {
  return hex(await crypto.subtle.digest("SHA-256", encoder.encode(data)));
}

export function sentSignature(request: Request): string | undefined {
  return /Signature=([0-9a-f]{64})$/.exec(request.headers.get("authorization") ?? "")?.[1];
}

export async function expectedSignature(request: Request, secret: string): Promise<string> {
  const authorization = request.headers.get("authorization") ?? "";
  const scope = /Credential=[^/]+\/(\d{8})\/([^/]+)\/([^/]+)\/aws4_request/.exec(authorization);
  const signedHeaders = /SignedHeaders=([^,]+)/.exec(authorization)?.[1] ?? "";
  const [, day, region, service] = scope ?? [];
  const amzDate = request.headers.get("x-amz-date") ?? "";
  const url = new URL(request.url);
  const canonicalHeaders = signedHeaders
    .split(";")
    .map((name) => `${name}:${name === "host" ? url.host : request.headers.get(name)}\n`)
    .join("");
  const canonical = [
    request.method,
    url.pathname,
    url.search.slice(1),
    canonicalHeaders,
    signedHeaders,
    request.headers.get("x-amz-content-sha256"),
  ].join("\n");
  const toSign = [
    "AWS4-HMAC-SHA256",
    amzDate,
    `${day}/${region}/${service}/aws4_request`,
    await sha256Hex(canonical),
  ].join("\n");
  let key = await hmac(`AWS4${secret}`, day);
  for (const part of [region, service, "aws4_request"]) key = await hmac(key, part);
  return hex(await hmac(key, toSign));
}
