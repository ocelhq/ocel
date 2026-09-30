import assert from "node:assert/strict";
import net from "node:net";
import tls from "node:tls";
import type { Check } from "./context";

const CLOUDFLARE_API = "https://api.cloudflare.com/client/v4";
const ORIGIN_TIMEOUT_MS = 10_000;
const ORIGIN_RECORD_TYPES = ["A", "AAAA", "CNAME"];
const CLIENT_CERTIFICATE_TLS_VERSION = "TLSv1.2";
const CLIENT_CERTIFICATE_REFUSALS =
  /certificate[ _]required|handshake[ _]failure|bad[ _]certificate|access[ _]denied/i;
const HUNG_UP = ["ECONNRESET", "EPIPE"];
const HOSTNAME_DECLINED = /unrecognized[ _]name|alert[ _]internal[ _]error/i;

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
    const forwarded = ORIGIN_RECORD_TYPES.map((type) =>
      records.find((record) => record.proxied && record.type === type),
    ).find((record) => record !== undefined);
    assert.ok(forwarded, `${hostname} has no proxied record in zone ${zone} naming its origin`);
    return forwarded.content;
  }
  throw new Error(`no Cloudflare zone the token reads serves ${hostname}`);
}

export type Origin = { address: string; port: number; hostname: string; timeoutMs?: number };

export type Outcome =
  | { kind: "unreachable"; reason: string }
  | { kind: "refused"; reason: string }
  | { kind: "closed"; reason: string }
  | { kind: "answered"; status: number | undefined; location: string | undefined; said: string }
  | { kind: "undecided"; reason: string };

export function statusOf(answer: string): number | undefined {
  const status = /^HTTP\/1\.[01] (\d{3})/.exec(answer)?.[1];
  return status === undefined ? undefined : Number(status);
}

function locationOf(answer: string): string | undefined {
  const head = answer.split("\r\n\r\n")[0] ?? "";
  return /^location:\s*(.+)$/im.exec(head)?.[1]?.trim();
}

function answered(said: string): Outcome {
  return { kind: "answered", status: statusOf(said), location: locationOf(said), said };
}

function reasonOf(error: Error & { code?: string }): string {
  return [error.code, error.message].filter(Boolean).join(": ");
}

function ask(
  origin: Origin,
  speak: (connected: net.Socket) => net.Socket,
  refusal: (error: Error & { code?: string }) => boolean,
): Promise<Outcome> {
  return new Promise((resolve) => {
    const raw = net.connect({ host: origin.address, port: origin.port });
    let connected = false;
    let spoken: net.Socket | undefined;
    let said = "";
    let settled = false;
    const settle = (outcome: Outcome) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      spoken?.destroy();
      raw.destroy();
      resolve(outcome);
    };
    const timer = setTimeout(
      () =>
        settle(
          connected
            ? {
                kind: "undecided",
                reason: `nothing decided within ${origin.timeoutMs ?? ORIGIN_TIMEOUT_MS}ms after connecting`,
              }
            : {
                kind: "unreachable",
                reason: `no connection within ${origin.timeoutMs ?? ORIGIN_TIMEOUT_MS}ms`,
              },
        ),
      origin.timeoutMs ?? ORIGIN_TIMEOUT_MS,
    );
    raw.once("error", (error: Error & { code?: string }) => {
      if (!connected) settle({ kind: "unreachable", reason: reasonOf(error) });
    });
    raw.once("connect", () => {
      connected = true;
      spoken = speak(raw);
      spoken.on("data", (chunk: Buffer) => {
        said += chunk.toString("latin1");
      });
      spoken.on("error", (error: Error & { code?: string }) => {
        if (said !== "") settle(answered(said));
        else if (refusal(error)) settle({ kind: "refused", reason: reasonOf(error) });
        else if (HUNG_UP.includes(error.code ?? ""))
          settle({ kind: "closed", reason: reasonOf(error) });
        else settle({ kind: "undecided", reason: reasonOf(error) });
      });
      spoken.on("end", () =>
        settle(
          said === "" ? { kind: "closed", reason: "closed with nothing said" } : answered(said),
        ),
      );
      spoken.on("close", () =>
        settle(
          said === "" ? { kind: "closed", reason: "closed with nothing said" } : answered(said),
        ),
      );
    });
  });
}

function request(hostname: string): string {
  return `GET / HTTP/1.1\r\nHost: ${hostname}\r\nConnection: close\r\n\r\n`;
}

export function clientCertificateRefused(error: Error & { code?: string }): boolean {
  return CLIENT_CERTIFICATE_REFUSALS.test(reasonOf(error));
}

export function hostnameDeclined(error: Error & { code?: string }): boolean {
  return (error.code ?? "").startsWith("ERR_SSL_") && HOSTNAME_DECLINED.test(reasonOf(error));
}

export type TLSAsking = {
  refusal: (error: Error & { code?: string }) => boolean;
  maxVersion: tls.SecureVersion;
};

export function askOverTLS(
  origin: Origin,
  { refusal, maxVersion }: TLSAsking = {
    refusal: clientCertificateRefused,
    maxVersion: CLIENT_CERTIFICATE_TLS_VERSION,
  },
): Promise<Outcome> {
  return ask(
    origin,
    (socket) => {
      const spoken = tls.connect({
        socket,
        servername: origin.hostname,
        rejectUnauthorized: false,
        maxVersion,
      });
      spoken.once("secureConnect", () => spoken.write(request(origin.hostname)));
      return spoken;
    },
    refusal,
  );
}

export function askOverPlainHTTP(origin: Origin): Promise<Outcome> {
  return ask(
    origin,
    (socket) => {
      socket.write(request(origin.hostname));
      return socket;
    },
    () => false,
  );
}

export function assertRefused(outcome: Outcome, where: string): void {
  if (outcome.kind === "refused") return;
  const seen =
    outcome.kind === "answered"
      ? `answered ${outcome.status ?? "without a status line"}:\n${outcome.said.slice(0, 300)}`
      : `${outcome.kind === "unreachable" ? "was never reached" : "connected and then"}: ${outcome.reason}`;
  throw new assert.AssertionError({
    message: `${where} was asked with no client certificate and ${seen}; want the TLS handshake refused for want of one`,
  });
}

export function assertRedirectedToHTTPS(outcome: Outcome, where: string, hostname: string): void {
  const want = `https://${hostname}/`;
  if (outcome.kind === "answered" && outcome.status === 308 && outcome.location === want) return;
  const seen =
    outcome.kind === "answered"
      ? `answered ${outcome.status ?? "without a status line"} to ${outcome.location ?? "nowhere"}:\n${outcome.said.slice(0, 300)}`
      : `${outcome.kind === "unreachable" ? "was never reached" : "connected and then"}: ${outcome.reason}`;
  throw new assert.AssertionError({
    message: `${where} over plain http ${seen}; want 308 to ${want}, forwarding nothing`,
  });
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
    const outcome = await askOverTLS({ address, port: 443, hostname });
    assertRefused(outcome, `${address}:443 for ${hostname}`);
  },
};

const redirectedOverPlainHTTP: Check = {
  title:
    "the origin redirects a request that skips Cloudflare over plain HTTP to https, forwarding nothing",
  run: async (ctx) => {
    const hostname = new URL(ctx.baseUrl).hostname;
    const address = await readOriginAddress(hostname, token());
    const outcome = await askOverPlainHTTP({ address, port: 80, hostname });
    assertRedirectedToHTTPS(outcome, `${address}:80 for ${hostname}`, hostname);
  },
};

export const SHIELDED_ORIGIN_CHECKS: Check[] = [refusedOverTLS, redirectedOverPlainHTTP];

export const CLIENT_CERTIFICATE_CHECKS: Check[] = [refusedOverTLS];
