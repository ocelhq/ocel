import { WorkerEntrypoint } from "cloudflare:workers";

import { bearer } from "@platform/cf-auth";

import { authorized } from "./auth";
import { DeploymentsStore } from "./deployments-do";
import type { Env } from "./env";
import type { PointerMove, PointerRecordResult } from "./store";

export { DeploymentsStore };

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

export default class extends WorkerEntrypoint<Env> {
  async fetch(request: Request): Promise<Response> {
    const url = new URL(request.url);
    const segments = url.pathname.split("/").filter(Boolean);
    if (segments.length < 2) return new Response("Not Found", { status: 404 });
    const slug = segments[0];
    const sub = `/${segments.slice(1).join("/")}`;
    const store = stub(this.env, slug);

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
      const outcome = await store.initialize(body.ownerToken, body.secret, body.force ?? false);
      if (outcome === "refused") {
        return new Response(
          `project ${slug} already has an identity; initialize with force to replace it`,
          { status: 409 },
        );
      }
      return new Response(null, { status: 204 });
    }

    const token = bearer(request);
    if (token === null || !(await store.authorized(token))) {
      return new Response("Unauthorized", { status: 401 });
    }

    if (request.method === "GET" && sub === "/pointer") {
      const pointer = url.searchParams.get("pointer") || undefined;
      return Response.json({ promotionId: (await store.readServedPromotion(pointer)) ?? null });
    }

    if (request.method === "POST" && sub === "/move-pointer") {
      const body = await readJson<PointerMove>(request);
      if (!body?.promotionId || !Array.isArray(body.records)) {
        return new Response("Bad Request", { status: 400 });
      }
      if ((await store.movePointer(body)) === "stale") {
        return new Response(
          `the pointer no longer serves ${body.replaces || "nothing"}, the promotion this pointer move replaces`,
          { status: 409 },
        );
      }
      return new Response(null, { status: 204 });
    }

    if (request.method === "GET" && sub === "/apps") {
      return Response.json(await store.listApps());
    }

    if (request.method === "GET" && sub === "/version-stamp") {
      return Response.json({ version: (await store.readVersionStamp()) ?? null });
    }

    if (request.method === "PUT" && sub === "/version-stamp") {
      const body = await readJson<{ version: string }>(request);
      if (!body?.version) return new Response("Bad Request", { status: 400 });
      await store.setVersionStamp(body.version);
      return new Response(null, { status: 204 });
    }

    if (request.method === "POST" && sub === "/remove-pointer") {
      const body = await readJson<{ pointer?: string }>(request);
      if (!body?.pointer) return new Response("Bad Request", { status: 400 });
      await store.removePointer(body.pointer);
      return new Response(null, { status: 204 });
    }

    if (request.method === "POST" && sub === "/destroy") {
      await store.destroy();
      return new Response(null, { status: 204 });
    }

    return new Response("Not Found", { status: 404 });
  }

  async readPointerRecord(args: {
    slug: string;
    app?: string;
    knownIdentity?: string;
  }): Promise<PointerRecordResult> {
    return stub(this.env, args.slug).readPointerRecord(args.app, args.knownIdentity);
  }

  async readLabelRecord(args: {
    slug: string;
    label: string;
    knownIdentity?: string;
  }): Promise<PointerRecordResult> {
    return stub(this.env, args.slug).readLabelRecord(args.label, args.knownIdentity);
  }
}
