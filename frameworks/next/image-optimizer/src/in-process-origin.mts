import type { ImageOriginRequest } from "@framework/next-protocol/image";
import type { ObjectStore } from "./store.mjs";

export function newInProcessImageOrigin(
  store: ObjectStore,
): (payload: ImageOriginRequest) => Promise<Response> {
  return async (payload) => {
    const { optimize } = await import("./optimize.mjs");
    const optimized = await optimize(payload, { store });
    return new Response(optimized.body, { status: optimized.status, headers: optimized.headers });
  };
}
