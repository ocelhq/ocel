import type { PurgeGroup } from "@platform/edge-contract/purge";
import { parseRenderCall, type RenderCoordinates, type ServeProps } from "../src/serve-key";
import type { Serving } from "../src/serve-prerender";

export function readRenderTarget(url: string): {
  coordinates: Required<RenderCoordinates>;
  originUrl: string;
} {
  const { source, ...coordinates } = parseRenderCall(url);
  return { coordinates, originUrl: source.kind === "image" ? source.optimizerUrl : source.url };
}

export interface ServeCall {
  request: Request;
  props: ServeProps;
  coordinates: Required<RenderCoordinates>;
  originUrl: string;
}

export interface FakeServing {
  serving: Serving;
  calls: ServeCall[];
  purges: { groups: PurgeGroup[]; awaited: boolean }[];
}

function isRedirect(response: Response): boolean {
  return response.status >= 300 && response.status < 400 && response.headers.has("location");
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
    slug: "p1",
    isrPrefix: "prod/p1/web/r0a1b2c3d/isr",
    serve: ({ props }) => ({
      fetch: async (request) => {
        const call = {
          request,
          props,
          ...readRenderTarget(request.url),
        };
        calls.push(call);
        const response = await answer(call);
        if (request.redirect !== "manual" && isRedirect(response)) {
          throw new TypeError(`workerd followed Serve's ${response.status} back into Serve`);
        }
        return response;
      },
      purge: async () => ({ ok: true }),
    }),
    purge: async (groups, awaited) => {
      purges.push({ groups, awaited });
    },
  };
  return { serving, calls, purges };
}
