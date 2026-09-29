import assert from "node:assert/strict";
import net from "node:net";
import tls from "node:tls";
import type { Check } from "./context";

const CLOUDFLARE_API = "https://api.cloudflare.com/client/v4";
const ORIGIN_TIMEOUT_MS = 10_000;

type Listed<T> = { success: boolean; result: T[] };

async function listed<T>(path: string, token: string): Promise<T[]> {
  const res = await fetch(`${CLOUDFLARE_API}${path}`, {
    headers: { authorization: `Bearer ${token}` },
  });
  const body = (await res.json()) as Listed<T>;
  assert.ok(res.ok && body.success, `GET ${path} answered ${res.status}`);
  return body.result;
}

export async function readOriginAddress(hostname: string, token: string): Promise<string> {
  const labels = hostname.split(".");
  for (let at = 1; at < labels.length - 1; at++) {
    const zone = labels.slice(at).join(".");
    const [found] = await listed<{ id: string }>(`/zones?name=${zone}`, token);
    if (!found) continue;
    const records = await listed<{ type: string; content: string; proxied: boolean }>(
      `/zones/${found.id}/dns_records?name=${hostname}`,
      token,
    );
    const forwarded = records.find(
      (record) => record.proxied && ["A", "AAAA", "CNAME"].includes(record.type),
    );
    assert.ok(forwarded, `${hostname} has no proxied record in zone ${zone} naming its origin`);
    return forwarded.content;
  }
  throw new Error(`no Cloudflare zone the token reads serves ${hostname}`);
}

export function answerOverTLS(address: string, hostname: string): Promise<string> {
  return new Promise((resolve) => {
    const socket = tls.connect({
      host: address,
      port: 443,
      servername: hostname,
      rejectUnauthorized: false,
      timeout: ORIGIN_TIMEOUT_MS,
    });
    let said = "";
    const done = () => {
      socket.destroy();
      resolve(said);
    };
    socket.on("secureConnect", () => {
      socket.write(`GET / HTTP/1.1\r\nHost: ${hostname}\r\nConnection: close\r\n\r\n`);
    });
    socket.on("data", (chunk: Buffer) => {
      said += chunk.toString("latin1");
    });
    socket.on("timeout", done);
    socket.on("error", done);
    socket.on("end", done);
    socket.on("close", done);
  });
}

export function answerOverPlainHTTP(address: string, hostname: string): Promise<string> {
  return new Promise((resolve) => {
    const socket = net.connect({ host: address, port: 80, timeout: ORIGIN_TIMEOUT_MS });
    let said = "";
    const done = () => {
      socket.destroy();
      resolve(said);
    };
    socket.on("connect", () => {
      socket.write(`GET / HTTP/1.1\r\nHost: ${hostname}\r\nConnection: close\r\n\r\n`);
    });
    socket.on("data", (chunk: Buffer) => {
      said += chunk.toString("latin1");
    });
    socket.on("timeout", done);
    socket.on("error", done);
    socket.on("end", done);
    socket.on("close", done);
  });
}

export function statusOf(answer: string): number | undefined {
  const status = /^HTTP\/1\.[01] (\d{3})/.exec(answer)?.[1];
  return status === undefined ? undefined : Number(status);
}

function token(): string {
  const read = process.env.CLOUDFLARE_API_TOKEN?.trim();
  assert.ok(
    read,
    "CLOUDFLARE_API_TOKEN is unset, and the origin a proxied record names is read with it",
  );
  return read;
}

const refusedOverTLS: Check = {
  title: "the origin refuses a request that skips Cloudflare, over TLS with no client certificate",
  run: async (ctx) => {
    const hostname = new URL(ctx.baseUrl).hostname;
    const address = await readOriginAddress(hostname, token());
    const answer = await answerOverTLS(address, hostname);
    assert.equal(
      statusOf(answer),
      undefined,
      `${address}:443 answered ${hostname} to a client presenting no certificate:\n${answer.slice(0, 300)}`,
    );
  },
};

const refusedOverPlainHTTP: Check = {
  title: "the origin refuses a request that skips Cloudflare, over plain HTTP",
  run: async (ctx) => {
    const hostname = new URL(ctx.baseUrl).hostname;
    const address = await readOriginAddress(hostname, token());
    const answer = await answerOverPlainHTTP(address, hostname);
    const status = statusOf(answer);
    assert.ok(
      status === undefined || status >= 400,
      `${address}:80 answered ${hostname} with ${status}, which no request that skips Cloudflare may reach:\n${answer.slice(0, 300)}`,
    );
  },
};

export const SHIELDED_ORIGIN_CHECKS: Check[] = [refusedOverTLS];

export const SHIELDED_BOX_CHECKS: Check[] = [refusedOverTLS, refusedOverPlainHTTP];
