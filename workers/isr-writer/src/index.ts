import { WorkerEntrypoint } from "cloudflare:workers";

import { bearer, isSecretHash, matchesHash, matchesSecret } from "./auth";
import { IsrDeploy } from "./isr-deploy";
import { entryObjectKey, writeEntry } from "./write";
import type { Env } from "./env";

export { IsrDeploy };

// One shared worker holds the DO namespace for the whole account; each deploy
// addresses its own instance by its isrPrefix (idFromName). The prefix is the
// leading path segments of every request, and the trailing segment is the op.
function stub(env: Env, isrPrefix: string) {
  return env.ISR_WRITER_DO.get(env.ISR_WRITER_DO.idFromName(isrPrefix));
}

// Hashes resolved from the registry, memoized per isolate so a steady stream of
// entry writes costs one DO round trip a minute rather than one per write. The
// memo is a cache, so it expires: without a bound, a retirement an isolate never
// saw would never take effect there. A minute is short next to the life of a
// build and long next to a burst of writes.
const MEMO_TTL_MS = 60_000;
const secretHashes = new Map<string, { hash: string; expiresAt: number }>();

function memoizedSecretHash(isrPrefix: string): string | undefined {
  const entry = secretHashes.get(isrPrefix);
  if (entry === undefined) return undefined;
  if (entry.expiresAt <= Date.now()) {
    secretHashes.delete(isrPrefix);
    return undefined;
  }
  return entry.hash;
}

function memoizeSecretHash(isrPrefix: string, hash: string): void {
  secretHashes.set(isrPrefix, { hash, expiresAt: Date.now() + MEMO_TTL_MS });
}

// Redeploying a build reseeds its registry with a freshly derived secret under
// the same isrPrefix, so a live memo can be a generation behind. A token that
// fails against the memo is therefore re-checked against the registry once
// before it is refused.
async function authorizedWrite(env: Env, isrPrefix: string, token: string): Promise<boolean> {
  const memoized = memoizedSecretHash(isrPrefix);
  if (memoized !== undefined && (await matchesHash(token, memoized))) return true;

  const hash = await stub(env, isrPrefix).secretHash();
  if (hash === undefined) {
    secretHashes.delete(isrPrefix);
    return false;
  }
  memoizeSecretHash(isrPrefix, hash);
  return hash !== memoized && (await matchesHash(token, hash));
}

async function readJson<T>(request: Request): Promise<T | undefined> {
  try {
    return (await request.json()) as T;
  } catch {
    return undefined;
  }
}

// The account-level ISR writer: a deployed Lambda's ISR entries reach R2
// through here rather than through standing R2 credentials of its own
// (epic decision 6). Every request names the deploy's isrPrefix as its leading
// path segments and the op as its last:
//
// - POST /<isrPrefix>/initialize seeds that deploy's write-secret hash. Like
//   the deployments store's own initialize it is authorized by the
//   account-level bootstrap credential, the only op that credential may
//   perform besides retiring the same instance.
// - POST /<isrPrefix>/destroy retires the deploy when its build is pruned.
// - PUT /<isrPrefix>/entry?key=<cache key> writes one ISR entry, authenticated
//   with that deploy's own write secret. The object key is derived here from
//   the authenticated prefix, so no caller can address another deploy's slice.
//
// Auth is verified at this boundary and nowhere else: the DO behind it is
// unauthenticated, matching workers/deployments-store/src/index.ts.
export default class extends WorkerEntrypoint<Env> {
  async fetch(request: Request): Promise<Response> {
    const url = new URL(request.url);
    const segments = url.pathname.split("/").filter(Boolean);
    if (segments.length < 2) return new Response("Not Found", { status: 404 });
    const op = segments[segments.length - 1];
    const isrPrefix = segments.slice(0, -1).join("/");

    if (request.method === "POST" && op === "initialize") {
      if (!(await this.bootstrapAuthorized(request))) {
        return new Response("Unauthorized", { status: 401 });
      }
      const body = await readJson<{ secretHash: string }>(request);
      if (!isSecretHash(body?.secretHash)) {
        return new Response("Bad Request", { status: 400 });
      }
      await stub(this.env, isrPrefix).initialize(body.secretHash);
      memoizeSecretHash(isrPrefix, body.secretHash);
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

    if (request.method === "PUT" && op === "entry") {
      const token = bearer(request);
      if (token === null || !(await authorizedWrite(this.env, isrPrefix, token))) {
        return new Response("Unauthorized", { status: 401 });
      }
      const key = url.searchParams.get("key") ?? "";
      const objectKey = entryObjectKey(isrPrefix, key);
      if (objectKey === null) return new Response("Bad Request", { status: 400 });
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
