import type { Env } from "../../src/env";
import { answerPurgeOnWorker } from "../../src/purge-request";
import type { ServedRecordResult, StoredRecord } from "../../src/releases";
import { Serve as ProductionServe } from "../../src/serve";
import { ORIGIN_PURGE_SCOPE } from "../../src/serve-key";

export { PurgeQueue } from "../../src/purge-queue";

interface HarnessEnv {
  OCEL_HARNESS_RELEASE: string;
  OCEL_HARNESS_ISR_PREFIX: string;
  OCEL_PURGE_SECRET: string;
}

const ENTRY_PATH = "/entry/";

export class Serve extends ProductionServe {
  override async fetch(request: Request): Promise<Response> {
    const { OCEL_HARNESS_RELEASE: release } = this.env as unknown as HarnessEnv;
    const tag = decodeURIComponent(new URL(request.url).pathname.slice(1));
    return new Response(crypto.randomUUID(), {
      headers: {
        "cloudflare-cdn-cache-control": "max-age=3600",
        "cache-control": "no-store",
        "cache-tag": `${release}|${tag}`,
      },
    });
  }
}

function servedBy(harness: HarnessEnv): Pick<Env, "RELEASES" | "OCEL_SLUG"> {
  const releases = {
    async readServedRecord({ isrPrefix }: { isrPrefix: string }): Promise<ServedRecordResult> {
      if (isrPrefix !== harness.OCEL_HARNESS_ISR_PREFIX) return { kind: "not-served" };
      return {
        kind: "record",
        record: { edge: { purgeSecret: harness.OCEL_PURGE_SECRET } } as StoredRecord,
      };
    },
  };
  return { RELEASES: releases as unknown as Env["RELEASES"], OCEL_SLUG: "purge-harness" };
}

async function readEntry(request: Request, ctx: ExecutionContext): Promise<Response> {
  const url = new URL(request.url);
  const serve = ctx.exports.Serve({ props: ORIGIN_PURGE_SCOPE });
  const res = await serve.fetch(
    new Request(`https://serve.ocel.invalid/${url.pathname.slice(ENTRY_PATH.length)}`),
  );
  return new Response(await res.text(), {
    headers: {
      "cache-control": "no-store",
      "x-serve-cache": res.headers.get("cf-cache-status") ?? "",
    },
  });
}

export default {
  async fetch(request, env, ctx): Promise<Response> {
    const harness = env as unknown as HarnessEnv;
    const { pathname } = new URL(request.url);
    if (request.method === "POST") return answerPurgeOnWorker(request, servedBy(harness), ctx);
    if (pathname.startsWith(ENTRY_PATH)) return readEntry(request, ctx);
    return new Response("Not Found", { status: 404 });
  },
} satisfies ExportedHandler<Env>;
