import type { NextHost } from "@framework/next-runtime/host";
import { readPortBind } from "@framework/node-runtime/host";

export function newGcpNextHost(env: NodeJS.ProcessEnv): NextHost {
  return { bind: readPortBind(env) };
}
