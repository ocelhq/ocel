import assert from "node:assert/strict";
import type { Leg } from "./spec";

export type Fetch = (input: string | URL | Request, init?: RequestInit) => Promise<Response>;

export const INITIAL_GREETING = "journey-hello";
export const REDEPLOY_GREETING = "redeployed";
export const SECRET_TOKEN = "journey-secret-never-in-a-body";
export const REDACTED = "<redacted>";

export const OCEL_SVG_BYTES = 365;
export const LARGE_RESPONSE_BYTES = 5 * 1024 * 1024;
export const UNCAPPED_BODY_BYTES = 5 * 1024 * 1024;
export const SLEEP_MS = 25_000;
export const LONG_SLEEP_MS = 45_000;

export type ContractContext = {
  app: string;
  baseUrl: string;
  greeting: string;
  largeBodyBytes: number;
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

export type Guarded = { fetch: Fetch; settle: () => Promise<void> };

const BODYLESS = new Set([101, 204, 205, 304]);
const TOKEN = Buffer.from(SECRET_TOKEN, "utf8");

async function scan(stream: ReadableStream<Uint8Array>): Promise<boolean> {
  const reader = stream.getReader();
  let carry = Buffer.alloc(0);
  for (;;) {
    const { done, value } = await reader.read();
    if (done) {
      return false;
    }
    const window = Buffer.concat([carry, value]);
    if (window.includes(TOKEN)) {
      await reader.cancel();
      return true;
    }
    carry = window.subarray(Math.max(0, window.byteLength - TOKEN.byteLength + 1));
  }
}

export function secretGuarded(inner: Fetch): Guarded {
  const leaks: string[] = [];
  const pending: Promise<void>[] = [];
  return {
    fetch: async (input, init) => {
      const res = await inner(input, init);
      if (res.body === null || BODYLESS.has(res.status)) {
        return res;
      }
      const [given, watched] = res.body.tee();
      pending.push(
        scan(watched).then(
          (leaked) => {
            if (leaked) {
              leaks.push(requestUrl(input));
            }
          },
          (error: unknown) => {
            leaks.push(`${requestUrl(input)} (unscanned: ${String(error)})`);
          },
        ),
      );
      return new Response(given, {
        status: res.status,
        statusText: res.statusText,
        headers: res.headers,
      });
    },
    settle: async () => {
      await Promise.all(pending.splice(0));
      const found = leaks.splice(0);
      assert.deepEqual(found, [], `${found.join(", ")} leaked the secret value in its body`);
    },
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
