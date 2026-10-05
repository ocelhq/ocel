import http from "node:http";
import type { Refresh, ScheduleRefresh } from "@framework/next-runtime/refresh";

export function newInstanceRefresh(origin: () => string): ScheduleRefresh {
  const inflight = new Map<string, Promise<void>>();
  return (refresh: Refresh) => {
    const key = `${refresh.lastModified} ${refresh.url}`;
    const running = inflight.get(key);
    if (running) return running;
    const rendering = render(origin(), refresh).finally(() => inflight.delete(key));
    inflight.set(key, rendering);
    return rendering;
  };
}

function render(origin: string, refresh: Refresh): Promise<void> {
  return new Promise((resolve, reject) => {
    const request = http.get(new URL(refresh.url, origin), { headers: refresh.headers }, (res) => {
      res.resume();
      res.on("error", reject);
      res.on("end", () => {
        const status = res.statusCode ?? 0;
        if (status >= 200 && status < 300) resolve();
        else reject(new Error(`the re-render of ${refresh.url} answered ${status}`));
      });
    });
    request.on("error", reject);
  });
}
