import type { PurgeGroup } from "@platform/edge-contract/purge";
import {
  buildOriginUrl,
  type OriginCoordinates,
  readOriginCall,
  type ServeProps,
} from "../src/serve-key";
import type { Serving } from "../src/serve-prerender";

export interface ServeCall {
  request: Request;
  props: ServeProps;
  coordinates: Required<OriginCoordinates>;
  originUrl: string;
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
    host: "shop.example.com",
    release: "r0a1b2c3d",
    serve: ({ props }) => ({
      fetch: async (request) => {
        const call = {
          request,
          props,
          coordinates: readOriginCall(request.url),
          originUrl: buildOriginUrl(request.url),
        };
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
