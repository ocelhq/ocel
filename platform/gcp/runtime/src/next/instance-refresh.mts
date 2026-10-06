import type { Refresh, ScheduleRefresh } from "@framework/next-runtime/refresh";
import { renderAtOrigin } from "./loopback-render.mjs";

export function newInstanceRefresh(origin: () => string, timeoutMs: number): ScheduleRefresh {
  const inflight = new Map<string, Promise<void>>();
  return (refresh: Refresh) => {
    const key = `${refresh.lastModified} ${refresh.url}`;
    const running = inflight.get(key);
    if (running) return running;
    const rendering = Promise.resolve()
      .then(() => renderAtOrigin(origin(), refresh, timeoutMs))
      .finally(() => inflight.delete(key));
    inflight.set(key, rendering);
    return rendering;
  };
}
