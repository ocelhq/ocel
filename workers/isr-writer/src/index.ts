import { entryObjectKey } from "@ocel/next-cache";
import { WorkerEntrypoint } from "cloudflare:workers";

import { bearer, isSecretHash, matchesHash, matchesSecret } from "./auth";
import { readEntry, writeEntry } from "./entry";
import { IsrDeploy } from "./isr-deploy";
import type { Env } from "./env";

export { IsrDeploy };

// A deploy's isrPrefix, exactly: <env>/<project>/<app>/<buildId>. Every request
// path is this plus one op segment, and a path that is not is refused before the
// DO namespace is touched — idFromName materializes storage for whatever name it
// is handed, and nothing has authenticated the caller yet at that point.
const PREFIX_SEGMENTS = 4;
const PREFIX_SEGMENT = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;

function deployPrefix(segments: string[]): string | null {
  if (segments.length !== PREFIX_SEGMENTS) return null;
  return segments.every((s) => PREFIX_SEGMENT.test(s)) ? segments.join("/") : null;
}

function stub(env: Env, isrPrefix: string) {
  return env.ISR_WRITER_DO.get(env.ISR_WRITER_DO.idFromName(isrPrefix));
}

// Each deploy's secret hash, memoized per isolate so a steady stream of entry
// writes costs one DO round trip a minute rather than one per write. An absent
// hash — never seeded, or retired — is memoized too, so garbage aimed at a
// prefix nobody deployed cannot buy round trips to a single-threaded DO either.
//
// Two consequences, both deliberate and both bounded by MEMO_TTL_MS:
//
// - `refreshed` records that a token has already failed against this entry and
//   spent its one registry re-read. That re-read is what lets a redeploy's
//   freshly derived secret in immediately; every further failure is refused off
//   the memo, so a caller holding a bad token cannot drive DO load.
// - a retirement this isolate never handled takes effect here only once the memo
//   lapses, so `destroy` keeps authorizing writes for up to MEMO_TTL_MS in
//   isolates that did not serve it. Closing that window would mean consulting the
//   registry on every write, which is what the memo exists to avoid.
const MEMO_TTL_MS = 60_000;

interface Memo {
  hash: string | undefined;
  expiresAt: number;
  refreshed: boolean;
}

const secretHashes = new Map<string, Memo>();

function memoized(isrPrefix: string): Memo | undefined {
  const memo = secretHashes.get(isrPrefix);
  if (memo === undefined) return undefined;
  if (memo.expiresAt <= Date.now()) {
    secretHashes.delete(isrPrefix);
    return undefined;
  }
  return memo;
}

function memoize(isrPrefix: string, hash: string | undefined, refreshed: boolean): Memo {
  const memo = { hash, expiresAt: Date.now() + MEMO_TTL_MS, refreshed };
  secretHashes.set(isrPrefix, memo);
  return memo;
}

async function fromRegistry(
  env: Env,
  isrPrefix: string,
  refreshed: boolean,
): Promise<Memo> {
  return memoize(isrPrefix, await stub(env, isrPrefix).secretHash(), refreshed);
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
      secretHashes.delete(isrPrefix);
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
        if (body === null) return new Response("Not Found", { status: 404 });
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
