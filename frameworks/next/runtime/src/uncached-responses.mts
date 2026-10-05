import type { ResponseCache } from "@framework/next-router/http-cache";

export function uncachedResponses(): ResponseCache {
  return {
    match: async () => undefined,
    put: async (_request, response) => {
      await response.body?.cancel();
    },
  };
}
