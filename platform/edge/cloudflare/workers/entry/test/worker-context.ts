import { createExecutionContext } from "cloudflare:test";
import type { PurgeQueuePort } from "../src/purge-request";
import type { ServeProps } from "../src/serve-key";
import type { ServeFactory } from "../src/serve-objects";

export interface FakePurgeQueues {
  getByName(name: string): Partial<PurgeQueuePort>;
}

export function purgeQueuesAnswering(
  queue: (name: string) => Partial<PurgeQueuePort> = () => ({}),
): FakePurgeQueues {
  return {
    getByName: (name) => ({
      raise: async () => {},
      readTimes: async () => ({}),
      ...queue(name),
    }),
  };
}

export function serveAnswering(
  answer: (request: Request, props: ServeProps) => Response | Promise<Response>,
): ServeFactory {
  return ({ props }) => ({
    fetch: async (request) => answer(request, props),
    purge: async () => ({ ok: true }),
  });
}

export function contextWithExports(
  exports: { Serve?: ServeFactory; PurgeQueue?: FakePurgeQueues } = {},
): ExecutionContext {
  const ctx = createExecutionContext();
  Object.defineProperty(ctx, "exports", {
    value: {
      Serve: exports.Serve ?? serveAnswering(() => new Response("Not Found", { status: 404 })),
      PurgeQueue: exports.PurgeQueue ?? purgeQueuesAnswering(),
    },
  });
  return ctx;
}
