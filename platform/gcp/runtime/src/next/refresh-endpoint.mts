import type http from "node:http";
import type { CacheEntryFile } from "@framework/next-cache";
import { type IdTokenCheck, newGoogleIdTokenCheck } from "./google-id-token.mjs";
import { renderAtOrigin } from "./loopback-render.mjs";
import type { RefreshEnv } from "./refresh-env.mjs";
import { isRefreshTaskSignedBy, refreshSignatureHeader } from "./refresh-signature.mjs";
import { readRefreshTask } from "./refresh-task.mjs";

export type RefreshEndpoint = (
  req: http.IncomingMessage,
  res: http.ServerResponse,
) => Promise<boolean>;

export interface RefreshEndpointOptions {
  path: string;
  isrPrefix: string;
  secret: string;
  localOrigin: string;
  check: IdTokenCheck;
  readEntry: (key: string) => Promise<CacheEntryFile | null>;
  renderTimeoutMs?: number;
  readBackTimeoutMs?: number;
}

const defaultRenderTimeoutMs = 30_000;
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

function readBody(req: http.IncomingMessage): Promise<Buffer | undefined> {
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
    req.on("end", () => resolve(Buffer.concat(chunks)));
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
    let body: Buffer | undefined;
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
    if (!isRefreshTaskSignedBy(options.secret, body, req.headers[refreshSignatureHeader])) {
      console.warn("ocel: dropped a refresh task this revision did not sign");
      answer(res, 204);
      return true;
    }
    const task = readRefreshTask(body.toString("utf8"));
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
  refresh: RefreshEnv | undefined,
  localOrigin: string,
  readEntry: RefreshEndpointOptions["readEntry"],
): RefreshEndpoint | undefined {
  if (!refresh) return undefined;
  return newRefreshEndpoint({
    path: new URL(refresh.url).pathname,
    isrPrefix: refresh.isrPrefix,
    secret: refresh.secret,
    localOrigin,
    readEntry,
    check: newGoogleIdTokenCheck({ audience: refresh.url, email: refresh.account }),
  });
}
