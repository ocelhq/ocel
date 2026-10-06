import { type DispatchHost, dispatchRequest } from "@framework/next-runtime/dispatch-host";
import { fetchToNodeHandler } from "@framework/node-runtime/fetch-bridge";
import type { Invoke } from "@framework/node-runtime/host";

const routerStateTree = "next-router-state-tree";

export function trimVaryForCloudCdn(vary: string | null): string | null {
  if (vary === null) return null;
  const seen = new Set<string>();
  const kept: string[] = [];
  for (const token of vary.split(",")) {
    const name = token.trim();
    const key = name.toLowerCase();
    if (name === "" || key === routerStateTree || seen.has(key)) continue;
    seen.add(key);
    kept.push(name);
  }
  return kept.length > 0 ? kept.join(", ") : null;
}

export function withCloudCdnVary(response: Response): Response {
  const current = response.headers.get("vary");
  const trimmed = trimVaryForCloudCdn(current);
  if (trimmed === current) return response;
  const rewritten = new Response(response.body, response);
  if (trimmed === null) rewritten.headers.delete("vary");
  else rewritten.headers.set("vary", trimmed);
  return rewritten;
}

export async function dispatchForCloudCdn(
  request: Request,
  host: DispatchHost,
  waitUntil: (promise: Promise<unknown>) => void,
): Promise<Response> {
  return withCloudCdnVary(await dispatchRequest(request, host, waitUntil));
}

export function newCloudCdnDispatchInvoke(host: DispatchHost): Invoke {
  return (req, res, ocel) =>
    fetchToNodeHandler((request) => dispatchForCloudCdn(request, host, ocel.waitUntil))(
      req,
      res,
      ocel,
    );
}
