import http from "node:http";
import { refreshHeader } from "@framework/next-cache";
import type { Refresh } from "@framework/next-runtime/refresh";

export function renderAtOrigin(origin: string, refresh: Refresh, timeoutMs: number): Promise<void> {
  return new Promise((resolve, reject) => {
    let timer: NodeJS.Timeout | undefined;
    let settled = false;
    const settle = (done: () => void) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      done();
    };
    let request: http.ClientRequest;
    try {
      const target = new URL(refresh.url, origin);
      if (target.origin !== new URL(origin).origin) {
        throw new Error(`the re-render of ${refresh.url} leaves ${origin}`);
      }
      const host = refresh.headers.host;
      request = http.get(
        target,
        {
          headers: {
            ...(typeof host === "string" ? { host } : {}),
            [refreshHeader]: String(refresh.lastModified),
          },
        },
        (res) => {
          res.resume();
          res.on("error", (err) => settle(() => reject(err)));
          res.on("close", () => {
            if (!res.complete) {
              settle(() =>
                reject(new Error(`the re-render of ${refresh.url} closed before its end`)),
              );
            }
          });
          res.on("end", () => {
            const status = res.statusCode ?? 0;
            if (status >= 200 && status < 300) settle(resolve);
            else
              settle(() => reject(new Error(`the re-render of ${refresh.url} answered ${status}`)));
          });
        },
      );
    } catch (err) {
      reject(err);
      return;
    }
    timer = setTimeout(() => {
      settle(() =>
        reject(new Error(`the re-render of ${refresh.url} did not answer within ${timeoutMs}ms`)),
      );
      request.destroy();
    }, timeoutMs);
    request.on("error", (err) => settle(() => reject(err)));
  });
}
