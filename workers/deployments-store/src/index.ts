import { WorkerEntrypoint } from "cloudflare:workers";

import { authorized, bearer } from "./auth";
import { DeploymentsStore } from "./deployments-do";
import type { DeploymentRecord, PointerRecordResult, Promotion } from "./store";
import type { Env } from "./env";

export { DeploymentsStore };

// One shared worker holds the DO namespace for the whole account; each project
// addresses its own instance by its slug (idFromName). Every request names the
// slug as the leading path segment (fetch) or the leading RPC argument.
function stub(env: Env, slug: string) {
  return env.DEPLOYMENTS_DO.get(env.DEPLOYMENTS_DO.idFromName(slug));
}

async function readJson<T>(request: Request): Promise<T | undefined> {
  try {
    return (await request.json()) as T;
  } catch {
    return undefined;
  }
}

// The deployments store's two access paths (ADR 0002), now against a shared
// worker routed per project by slug:
//
// - fetch() is the authenticated write endpoint the deploy host calls over
//   plain HTTP. Routes are prefixed with the project slug (/<slug>/...). The
//   /<slug>/initialize route is authorized by the account-level bootstrap
//   credential (the only op that credential may perform); every other route
//   authenticates against the addressed instance's own stored project secret.
// - pointerRecord is the single RPC method the frozen generic worker calls
//   through its service binding, carrying the project slug: it resolves the
//   app's build id and record for a pointer (the reserved default, or a named
//   preview pointer) in one round trip, echoing the caller's knownBuildId back
//   unchanged to skip re-sending an unchanged record. It stays secret-less — the
//   trust boundary is the binding itself, only ever reachable from another
//   Worker in the same account.
export default class extends WorkerEntrypoint<Env> {
  async fetch(request: Request): Promise<Response> {
    const url = new URL(request.url);
    const segments = url.pathname.split("/").filter(Boolean);
    if (segments.length < 2) return new Response("Not Found", { status: 404 });
    const slug = segments[0];
    const sub = "/" + segments.slice(1).join("/");
    const store = stub(this.env, slug);

    // Seeding/rotating a project's instance is the sole op the account-level
    // bootstrap credential authorizes.
    if (request.method === "POST" && sub === "/initialize") {
      if (!(await authorized(request, this.env.BOOTSTRAP_SECRET))) {
        return new Response("Unauthorized", { status: 401 });
      }
      const body = await readJson<{
        ownerToken: string;
        secret: string;
        force?: boolean;
      }>(request);
      if (!body?.ownerToken || !body.secret) {
        return new Response("Bad Request", { status: 400 });
      }
      const { conflict } = await store.initialize(
        body.ownerToken,
        body.secret,
        body.force ?? false,
      );
      if (conflict) return new Response(conflict, { status: 409 });
      return new Response(null, { status: 204 });
    }

    // Every other op authenticates against the instance's own project secret.
    const token = bearer(request);
    if (token === null || !(await store.authorized(token))) {
      return new Response("Unauthorized", { status: 401 });
    }

    if (request.method === "PUT" && sub === "/staged") {
      const record = await readJson<DeploymentRecord>(request);
      if (!record) return new Response("Bad Request", { status: 400 });
      await store.putStaged(record);
      return new Response(null, { status: 204 });
    }

    if (request.method === "POST" && sub === "/promote") {
      const body = await readJson<Promotion & { pointer?: string }>(request);
      if (!body?.promotionId || !body.builds) {
        return new Response("Bad Request", { status: 400 });
      }
      // The pointer to move is an argument to the promote operation, not part of
      // the persisted Promotion; the store defaults it when absent.
      const { pointer, ...promotion } = body;
      const { conflict } = await store.promote(promotion, pointer);
      if (conflict) return new Response(conflict, { status: 409 });
      return new Response(null, { status: 204 });
    }

    if (request.method === "GET" && sub === "/history") {
      // The pointer to scope history to is an optional query param; absent means
      // the reserved default (production).
      const pointer = url.searchParams.get("pointer") ?? undefined;
      return Response.json(await store.history(pointer));
    }

    if (request.method === "POST" && sub === "/prune") {
      const body = await readJson<{ keepN: number; pointer?: string }>(request);
      if (typeof body?.keepN !== "number") {
        return new Response("Bad Request", { status: 400 });
      }
      return Response.json(await store.prune(body.keepN, body.pointer));
    }

    if (request.method === "GET" && sub === "/version-stamp") {
      return Response.json({ version: (await store.versionStamp()) ?? null });
    }

    if (request.method === "PUT" && sub === "/version-stamp") {
      const body = await readJson<{ version: string }>(request);
      if (!body?.version) return new Response("Bad Request", { status: 400 });
      await store.setVersionStamp(body.version);
      return new Response(null, { status: 204 });
    }

    if (request.method === "POST" && sub === "/remove-pointer") {
      // Full teardown of one pointer (a `preview rm`): unlike /prune it pins
      // nothing. The pointer to remove is required — an absent one would wipe
      // the reserved production default, which this op must never do implicitly.
      const body = await readJson<{ pointer?: string }>(request);
      if (!body?.pointer) return new Response("Bad Request", { status: 400 });
      return Response.json(await store.removePointer(body.pointer));
    }

    if (request.method === "POST" && sub === "/destroy") {
      await store.destroy();
      return new Response(null, { status: 204 });
    }

    return new Response("Not Found", { status: 404 });
  }

  async pointerRecord(args: {
    slug: string;
    app: string;
    pointer?: string;
    knownBuildId?: string;
  }): Promise<PointerRecordResult> {
    return stub(this.env, args.slug).pointerRecord(
      args.app,
      args.pointer,
      args.knownBuildId,
    );
  }
}
