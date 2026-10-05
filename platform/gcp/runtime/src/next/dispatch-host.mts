import type { RoutingManifest } from "@framework/next-protocol/routing-manifest";
import {
  type DispatchHost,
  newDispatchInvoke,
  readDispatchHost,
} from "@framework/next-runtime/dispatch-host";
import type { Invoke } from "@framework/node-runtime/host";

function functionIds(manifest: RoutingManifest): string[] {
  const ids = new Set<string>([manifest.entry]);
  for (const target of Object.values(manifest.dispatch)) {
    if ((target.kind === "function" || target.kind === "prerender") && target.id) {
      ids.add(target.id);
    }
  }
  if (manifest.middleware?.runtime === "nodejs") ids.add(manifest.middleware.id);
  return [...ids];
}

export function readGcpDispatchHost(env: NodeJS.ProcessEnv, localOrigin: string): DispatchHost {
  const host = readDispatchHost(env, localOrigin, { originFetch: fetch });
  return {
    ...host,
    functionUrls: Object.fromEntries(functionIds(host.manifest).map((id) => [id, localOrigin])),
  };
}

export function newGcpDispatchInvoke(localOrigin: string): Invoke {
  return newDispatchInvoke(readGcpDispatchHost(process.env, localOrigin));
}
