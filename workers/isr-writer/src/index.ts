import { entryMissHeader, entryObjectKey } from "@ocel/next-cache";
import { WorkerEntrypoint } from "cloudflare:workers";

import { bearer, isSecretHash, matchesHash, matchesSecret } from "./auth";
import { readEntry, writeEntry } from "./entry";
import { IsrDeploy } from "./isr-deploy";
import { forget, memoize, memoized } from "./memo";
import type { Env } from "./env";
import type { Memo } from "./memo";

export { IsrDeploy };

// A deploy's isrPrefix, exactly: <env>/<project>/<app>/<buildId>. Every request
// path is this plus one op segment. The entry op reaches the object named by
// that prefix before it has authenticated anyone, so the shape is checked first
// and the object writes no storage until an initialize (see registry.ts).
const PREFIX_SEGMENTS = 4;
const PREFIX_SEGMENT = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;

function deployPrefix(segments: string[]): string | null {
  if (segments.length !== PREFIX_SEGMENTS) return null;
  return segments.every((s) => PREFIX_SEGMENT.test(s)) ? segments.join("/") : null;
}

function stub(env: Env, isrPrefix: string) {
  return env.ISR_WRITER_DO.get(env.ISR_WRITER_DO.idFromName(isrPrefix));
}

// Reads in flight, so a herd arriving on one deploy before its memo is filled
// shares a single round trip instead of queueing one apiece at a
// single-threaded object. A read that rejects is not left here: the next
// request starts a new one.
const registryReads = new Map<string, Promise<Memo>>();

function fromRegistry(env: Env, isrPrefix: string, refreshed: boolean): Promise<Memo> {
  const inFlight = registryReads.get(isrPrefix);
  if (inFlight !== undefined) return inFlight;

  const read = stub(env, isrPrefix)
    .secretHash()
    .then((hash) => memoize(isrPrefix, hash, refreshed))
    .finally(() => registryReads.delete(isrPrefix));
  registryReads.set(isrPrefix, read);
  return read;
}

async function matches(memo: Memo, token: string): Promise<boolean> {
  return memo.hash !== undefined && (await matchesHash(token, memo.hash));
}

async function authorized(env: Env, isrPrefix: string, token: string): Promise<boolean> {
  const memo = memoized(isrPrefix) ?? (await fromRegistry(env, isrPrefix, false));
  if (await matches(memo, token)) return true;
  if (memo.refreshed) return false;
  return matches(await fromRegistry(env, isrPrefix, true), token);
}

async function readJson<T>(request: Request): Promise<T | undefined> {
  try {
    return (await request.json()) as T;
  } catch {
    return undefined;
  }
}

// The account-level ISR writer: a deployed Lambda's ISR entries reach R2 through
// here rather than through standing R2 credentials of its own (epic decision 6).
// Every request names the deploy's isrPrefix as its leading path segments and the
// op as its last:
//
// - POST /<isrPrefix>/initialize seeds that deploy's write-secret hash, and
//   POST /<isrPrefix>/destroy retires it when its build is pruned. Both are
//   authorized by the account-level bootstrap credential, as the deployments
//   store's own initialize is, and it authorizes nothing else.
// - PUT /<isrPrefix>/entry?key=<cache key> writes one ISR entry and
//   GET /<isrPrefix>/entry?key=<cache key> reads one back, both authenticated
//   with that deploy's own write secret. The object key is derived here from the
//   authenticated prefix, so no caller can address another deploy's slice.
//   Reads run through here as well as writes so the deployed function holds no
//   standing R2 credential for entries at all — a bucket-scoped token left on
//   the Lambda for reads would still be a bucket-scoped token, and R2 tokens
//   have no key-prefix grammar to narrow it with.
//
// Auth is verified at this boundary and nowhere else: the DO behind it is
// unauthenticated, matching workers/deployments-store/src/index.ts.
export default class extends WorkerEntrypoint<Env> {
  async fetch(request: Request): Promise<Response> {
    const url = new URL(request.url);
    const segments = url.pathname.split("/").filter(Boolean);
    const op = segments.pop();
    const isrPrefix = deployPrefix(segments);
    if (op === undefined || isrPrefix === null) {
      return new Response("Not Found", { status: 404 });
    }

    if (request.method === "POST" && op === "initialize") {
      if (!(await this.bootstrapAuthorized(request))) {
        return new Response("Unauthorized", { status: 401 });
      }
      const body = await readJson<{ secretHash: string }>(request);
      if (!isSecretHash(body?.secretHash)) {
        return new Response("Bad Request", { status: 400 });
      }
      await stub(this.env, isrPrefix).initialize(body.secretHash);
      // Not `refreshed`: an isolate that served a redeploy's predecessor still
      // owes the new generation its one registry re-read.
      memoize(isrPrefix, body.secretHash, false);
      return new Response(null, { status: 204 });
    }

    if (request.method === "POST" && op === "destroy") {
      if (!(await this.bootstrapAuthorized(request))) {
        return new Response("Unauthorized", { status: 401 });
      }
      await stub(this.env, isrPrefix).destroy();
      forget(isrPrefix);
      return new Response(null, { status: 204 });
    }

    if ((request.method === "PUT" || request.method === "GET") && op === "entry") {
      const token = bearer(request);
      if (token === null || !(await authorized(this.env, isrPrefix, token))) {
        return new Response("Unauthorized", { status: 401 });
      }
      const objectKey = entryObjectKey(isrPrefix, url.searchParams.get("key") ?? "");
      if (objectKey === null) return new Response("Bad Request", { status: 400 });

      if (request.method === "GET") {
        const body = await readEntry(this.env.OCEL_CACHE_STORE, objectKey);
        if (body === null) {
          return new Response("Not Found", {
            status: 404,
            headers: { [entryMissHeader]: "1" },
          });
        }
        return new Response(body, {
          headers: { "content-type": "application/json" },
        });
      }

      const outcome = await writeEntry(
        this.env.OCEL_CACHE_STORE,
        objectKey,
        await request.text(),
      );
      // A rate-limited write is reported, not retried: the caller already holds
      // a fresh render and the winner wrote one just as fresh.
      if (outcome === "rate-limited") {
        return new Response("Too Many Requests", { status: 429 });
      }
      return new Response(null, { status: 204 });
    }

    return new Response("Not Found", { status: 404 });
  }

  private async bootstrapAuthorized(request: Request): Promise<boolean> {
    const token = bearer(request);
    return token !== null && (await matchesSecret(token, this.env.BOOTSTRAP_SECRET));
  }
}
