import type { PurgeGroup } from "@platform/edge-contract/purge";
import type { ServeProps } from "../src/serve-key";
import type { Serving } from "../src/serve-prerender";

export interface ServeCall {
  request: Request;
  props: ServeProps;
}

export interface FakeServing {
  serving: Serving;
  calls: ServeCall[];
  purges: { groups: PurgeGroup[]; awaited: boolean }[];
}

export function fakeServing(
  answer: (call: ServeCall) => Response | Promise<Response> = () =>
    new Response("page", { headers: { "cf-cache-status": "MISS" } }),
): FakeServing {
  const calls: ServeCall[] = [];
  const purges: { groups: PurgeGroup[]; awaited: boolean }[] = [];
  const serving: Serving = {
    release: "r0a1b2c3d",
    serve: ({ props }) => ({
      fetch: async (request) => {
        const call = { request, props };
        calls.push(call);
        return answer(call);
      },
      purge: async () => ({ ok: true }),
    }),
    purge: async (groups, awaited) => {
      purges.push({ groups, awaited });
    },
  };
  return { serving, calls, purges };
}
