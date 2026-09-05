import type { DeploymentRecord } from "../src/deployments";

export const FN_URL = "https://abc123.lambda-url.eu-west-2.on.aws/";

export function makeRecord(
  over: Partial<DeploymentRecord> = {},
): DeploymentRecord {
  return {
    app: "api",
    runtime: "node",
    identity: "deploy-1",
    deploymentId: "deploy-1",
    routingManifest: null,
    functionUrls: { api: FN_URL },
    assetPrefix: "",
    isrPrefix: "",
    createdAt: 1_000,
    ...over,
  };
}

export function capturing(): { calls: Request[]; fetch: typeof fetch } {
  const calls: Request[] = [];
  return {
    calls,
    fetch: (async (input: RequestInfo | URL, init?: RequestInit) => {
      calls.push(new Request(input as RequestInfo, init));
      return new Response("origin");
    }) as typeof fetch,
  };
}

export async function withGlobalFetch<T>(
  replacement: typeof fetch,
  run: () => Promise<T>,
): Promise<T> {
  const original = globalThis.fetch;
  globalThis.fetch = replacement;
  try {
    return await run();
  } finally {
    globalThis.fetch = original;
  }
}
