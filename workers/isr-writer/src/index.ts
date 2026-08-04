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

// Hashes resolved from the registry, memoized for the life of the isolate so a
// steady stream of entry writes costs one DO round trip rather than one per
// write. Only a hash that exists is memoized: an uninitialized deploy must stay
// resolvable once its initialize lands. A retired deploy's entry survives in
// isolates that never saw the retirement, which costs nothing — the Lambda
// holding that secret is destroyed by the same prune.
const secretHashes = new Map<string, string>();

async function secretHashFor(env: Env, isrPrefix: string): Promise<string | undefined> {
  const memoized = secretHashes.get(isrPrefix);
  if (memoized !== undefined) return memoized;
  const hash = await stub(env, isrPrefix).secretHash();
  if (hash !== undefined) secretHashes.set(isrPrefix, hash);
  return hash;
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
      secretHashes.set(isrPrefix, body.secretHash);
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
      if (token === null) return new Response("Unauthorized", { status: 401 });
      const hash = await secretHashFor(this.env, isrPrefix);
      if (hash === undefined || !(await matchesHash(token, hash))) {
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
