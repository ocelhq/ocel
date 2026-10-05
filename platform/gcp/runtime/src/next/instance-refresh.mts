import http from "node:http";
import type { Refresh, ScheduleRefresh } from "@framework/next-runtime/refresh";

export function newInstanceRefresh(origin: () => string, timeoutMs: number): ScheduleRefresh {
  const inflight = new Map<string, Promise<void>>();
  return (refresh: Refresh) => {
    const key = `${refresh.lastModified} ${refresh.url}`;
    const running = inflight.get(key);
    if (running) return running;
    const rendering = Promise.resolve()
      .then(() => render(origin(), refresh, timeoutMs))
      .finally(() => inflight.delete(key));
    inflight.set(key, rendering);
    return rendering;
  };
}

function render(origin: string, refresh: Refresh, timeoutMs: number): Promise<void> {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      reject(new Error(`the re-render of ${refresh.url} did not answer within ${timeoutMs}ms`));
      request.destroy();
    }, timeoutMs);
    const settle = (done: () => void) => {
      clearTimeout(timer);
      done();
    };
    const request = http.get(new URL(refresh.url, origin), { headers: refresh.headers }, (res) => {
      res.resume();
      res.on("error", (err) => settle(() => reject(err)));
      res.on("end", () => {
        const status = res.statusCode ?? 0;
        if (status >= 200 && status < 300) settle(resolve);
        else settle(() => reject(new Error(`the re-render of ${refresh.url} answered ${status}`)));
      });
    });
    request.on("error", (err) => settle(() => reject(err)));
  });
}
