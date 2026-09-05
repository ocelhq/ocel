import assert from "node:assert/strict";
import type { Leg } from "./spec";

export type Fetch = (input: string | URL | Request, init?: RequestInit) => Promise<Response>;

export const INITIAL_GREETING = "journey-hello";
export const REDEPLOY_GREETING = "redeployed";
export const SECRET_TOKEN = "journey-secret-never-in-a-body";
export const REDACTED = "<redacted>";

export const OCEL_SVG_BYTES = 365;
export const LARGE_BYTES = 5 * 1024 * 1024;
export const SLEEP_MS = 25_000;

export type ContractContext = {
  app: string;
  baseUrl: string;
  greeting: string;
  leg: Leg;
  notes: Map<string, string>;
  fetch: Fetch;
};

export type ContractRow = {
  title: string;
  run: (ctx: ContractContext) => Promise<void>;
};

export function redact(text: string): string {
  return text.split(SECRET_TOKEN).join(REDACTED);
}

function requestUrl(input: Parameters<Fetch>[0]): string {
  return input instanceof Request ? input.url : String(input);
}

export function secretGuarded(inner: Fetch): Fetch {
  return async (input, init) => {
    const res = await inner(input, init);
    const bytes = Buffer.from(await res.clone().arrayBuffer());
    assert.ok(
      !bytes.includes(SECRET_TOKEN),
      `${requestUrl(input)} leaked the secret value in its body`,
    );
    return res;
  };
}

export function describeResponse(res: Response, text: string): string {
  const headers = [...res.headers]
    .map(([name, value]) => `  ${name}: ${value}`)
    .sort()
    .join("\n");
  const body = text.length > 500 ? `${text.slice(0, 500)}... (${text.length} bytes)` : text;
  return `status ${res.status}\n${headers}\nbody: ${JSON.stringify(body)}`;
}

export async function json(ctx: ContractContext, path: string, init?: RequestInit) {
  const res = await ctx.fetch(`${ctx.baseUrl}${path}`, init);
  const text = await res.text();
  return { res, text, body: parseBody(path, res, text) };
}

function parseBody(path: string, res: Response, text: string) {
  if (text.length === 0) return undefined;
  try {
    return JSON.parse(text);
  } catch {
    return assert.fail(`${path} did not answer JSON\n${describeResponse(res, text)}`);
  }
}
