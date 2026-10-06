import type http from "node:http";
import type { CacheEntryFile } from "@framework/next-cache";
import { type IdTokenCheck, newGoogleIdTokenCheck } from "./google-id-token.mjs";
import { renderAtOrigin } from "./loopback-render.mjs";
import { readRefreshTask } from "./refresh-task.mjs";

export type RefreshEndpoint = (
  req: http.IncomingMessage,
  res: http.ServerResponse,
) => Promise<boolean>;

export interface RefreshEndpointOptions {
  path: string;
  isrPrefix: string;
  localOrigin: string;
  check: IdTokenCheck;
  readEntry: (key: string) => Promise<CacheEntryFile | null>;
  renderTimeoutMs?: number;
  readBackTimeoutMs?: number;
}

const defaultRenderTimeoutMs = 40_000;
const defaultReadBackTimeoutMs = 10_000;
const maxBodyBytes = 65_536;

function answer(res: http.ServerResponse, status: number, headers: http.OutgoingHttpHeaders = {}) {
  res.writeHead(status, headers);
  res.end();
}

function refuseOversize(req: http.IncomingMessage, res: http.ServerResponse): void {
  res.on("finish", () => req.destroy());
  answer(res, 413, { connection: "close" });
}

function readBody(req: http.IncomingMessage): Promise<string | undefined> {
  return new Promise((resolve, reject) => {
    const chunks: Buffer[] = [];
    let size = 0;
    req.on("data", (chunk: Buffer) => {
      size += chunk.length;
      if (size > maxBodyBytes) {
        req.removeAllListeners("data");
        resolve(undefined);
        return;
      }
      chunks.push(chunk);
    });
    req.on("end", () => resolve(Buffer.concat(chunks).toString("utf8")));
    req.on("error", reject);
  });
}

function readBack(
  readEntry: RefreshEndpointOptions["readEntry"],
  key: string,
  url: string,
  timeoutMs: number,
): Promise<CacheEntryFile | null> {
  let timer: NodeJS.Timeout | undefined;
  const expiry = new Promise<never>((_, reject) => {
    timer = setTimeout(
      () => reject(new Error(`the read-back of ${url} did not answer within ${timeoutMs}ms`)),
      timeoutMs,
    );
  });
  return Promise.race([readEntry(key), expiry]).finally(() => clearTimeout(timer));
}

function refuse(res: http.ServerResponse, status: number, message: string): void {
  res.writeHead(status, { "content-type": "text/plain" });
  res.end(message);
}

export function newRefreshEndpoint(options: RefreshEndpointOptions): RefreshEndpoint {
  const renderTimeoutMs = options.renderTimeoutMs ?? defaultRenderTimeoutMs;
  const readBackTimeoutMs = options.readBackTimeoutMs ?? defaultReadBackTimeoutMs;
  return async (req, res) => {
    if (new URL(req.url ?? "/", "http://x").pathname !== options.path) return false;
    if (req.method !== "POST") {
      answer(res, 405, { allow: "POST" });
      return true;
    }
    let verified: boolean;
    try {
      verified = await options.check(req.headers.authorization);
    } catch {
      req.resume();
      answer(res, 503);
      return true;
    }
    if (!verified) {
      req.resume();
      answer(res, 401);
      return true;
    }
    if (Number(req.headers["content-length"]) > maxBodyBytes) {
      refuseOversize(req, res);
      return true;
    }
    let body: string | undefined;
    try {
      body = await readBody(req);
    } catch {
      answer(res, 400);
      return true;
    }
    if (body === undefined) {
      refuseOversize(req, res);
      return true;
    }
    const task = readRefreshTask(body);
    if (!task) {
      console.warn("ocel: dropped a refresh task this runtime cannot read");
      answer(res, 204);
      return true;
    }
    if (task.isrPrefix !== options.isrPrefix) {
      console.warn(
        `ocel: dropped a refresh task for ${task.isrPrefix}, this deployment is ${options.isrPrefix}`,
      );
      answer(res, 204);
      return true;
    }
    try {
      await renderAtOrigin(options.localOrigin, task.refresh, renderTimeoutMs);
    } catch (err) {
      refuse(res, 502, err instanceof Error ? err.message : "the re-render failed");
      return true;
    }
    const { url, key, lastModified } = task.refresh;
    let entry: CacheEntryFile | null;
    try {
      entry = await readBack(options.readEntry, key, url, readBackTimeoutMs);
    } catch (err) {
      refuse(res, 503, err instanceof Error ? err.message : `the read-back of ${url} failed`);
      return true;
    }
    if (!entry || entry.lastModified <= lastModified) {
      refuse(res, 500, `the re-render of ${url} left no entry newer than ${lastModified}`);
      return true;
    }
    answer(res, 204);
    return true;
  };
}

export function readRefreshEndpoint(
  env: NodeJS.ProcessEnv,
  localOrigin: string,
  readEntry: RefreshEndpointOptions["readEntry"],
): RefreshEndpoint | undefined {
  const url = env.OCEL_REFRESH_URL;
  if (!url) return undefined;
  const account = env.OCEL_REFRESH_ACCOUNT;
  if (!account) throw new Error("ocel: OCEL_REFRESH_URL is set but OCEL_REFRESH_ACCOUNT is not");
  const isrPrefix = env.OCEL_ISR_PREFIX;
  if (!isrPrefix) throw new Error("ocel: OCEL_REFRESH_URL is set but OCEL_ISR_PREFIX is not");
  return newRefreshEndpoint({
    path: new URL(url).pathname,
    isrPrefix,
    localOrigin,
    readEntry,
    check: newGoogleIdTokenCheck({ audience: url, email: account }),
  });
}
