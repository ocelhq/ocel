import type { PointerRecordResult, ReleaseRecord, ReleasesBinding } from "../src/releases";

export const FN_URL = "https://abc123.lambda-url.eu-west-2.on.aws/";

export function makeRecord(over: Partial<ReleaseRecord> = {}): ReleaseRecord {
  return {
    app: "api",
    framework: "node",
    release: "deploy-1",
    buildId: "deploy-1",
    routeTable: null,
    functionUrls: { api: FN_URL },
    assetPrefix: "",
    isrPrefix: "",
    createdAt: 1_000,
    ...over,
  };
}

export function answerEveryRecordWith(
  answer: (args: { slug: string; app?: string }) => Promise<PointerRecordResult>,
): ReleasesBinding {
  return { readPointerRecord: answer, readLabelRecord: answer };
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
