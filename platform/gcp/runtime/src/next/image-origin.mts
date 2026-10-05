import type { ObjectStore } from "@framework/next-image-optimizer/store";
import type { ImageOrigin } from "@framework/next-router/image";

export function newInProcessImageOrigin(store: ObjectStore): ImageOrigin {
  return async (payload) => {
    const { optimize } = await import("@framework/next-image-optimizer/optimize");
    const optimized = await optimize(payload, { store });
    return new Response(optimized.body, { status: optimized.status, headers: optimized.headers });
  };
}
