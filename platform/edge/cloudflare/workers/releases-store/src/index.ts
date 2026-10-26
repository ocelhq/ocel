import { WorkerEntrypoint } from "cloudflare:workers";

import { authorized, bearer } from "./auth";
import type { Env } from "./env";
import { putObjects, removeObjects } from "./objects";
import { ReleasesStore } from "./releases-do";
import type { KeptRelease, PointerMove, PointerRecordResult, ServedRecordResult } from "./store";

export { ReleasesStore };

function stub(env: Env, slug: string) {
  return env.RELEASES_DO.get(env.RELEASES_DO.idFromName(slug));
}

function isKeptReleases(kept: unknown): kept is KeptRelease[] {
  return (
    Array.isArray(kept) &&
    kept.every(
      (release: Partial<KeptRelease> | null) =>
        typeof release?.app === "string" &&
        release.app !== "" &&
        typeof release.releaseToken === "string" &&
        release.releaseToken !== "",
    )
  );
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
      const body = await readJson<{ secret: string }>(request);
      if (!body?.secret) return new Response("Bad Request", { status: 400 });
      if ((await store.initialize(body.secret)) === "refused") {
        return new Response(`project ${slug} already has an identity`, { status: 409 });
      }
      return new Response(null, { status: 204 });
    }

    if (request.method === "POST" && (sub === "/objects" || sub === "/objects/remove")) {
      if (!(await authorized(request, this.env.BOOTSTRAP_SECRET))) {
        return new Response("Unauthorized", { status: 401 });
      }
      if (!this.env.OBJECTS) return new Response("this store keeps no objects", { status: 404 });
      return sub === "/objects"
        ? putObjects(request, this.env.OBJECTS, slug)
        : removeObjects(request, this.env.OBJECTS, slug);
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
      if (!body?.promotionId || !Array.isArray(body.records) || !isKeptReleases(body.kept)) {
        return new Response("Bad Request", { status: 400 });
      }
      const outcome = await store.movePointer(body);
      if (outcome.kind === "stale") {
        return new Response(
          `the pointer no longer serves ${body.replaces || "nothing"}, the promotion this pointer move replaces`,
          { status: 409 },
        );
      }
      if (outcome.kind === "unlocated") {
        return new Response(
          `release ${outcome.release} of ${outcome.app} locates none of its objects, so no edge could serve its files`,
          { status: 422 },
        );
      }
      if (outcome.kind === "different-record") {
        return new Response(
          `release ${outcome.release} of ${outcome.app} is already served with another record, and a release's record never changes once it is served`,
          { status: 422 },
        );
      }
      return new Response(null, { status: 204 });
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

    if (request.method === "GET" && sub === "/gate") {
      const gate = await store.readGate();
      return Response.json({ gate: gate === undefined ? null : JSON.parse(gate) });
    }

    if (request.method === "PUT" && sub === "/gate") {
      const body = await readJson<unknown>(request);
      if (typeof body !== "object" || body === null || Array.isArray(body)) {
        return new Response("Bad Request", { status: 400 });
      }
      await store.setGate(JSON.stringify(body));
      return new Response(null, { status: 204 });
    }

    if (request.method === "DELETE" && sub === "/gate") {
      await store.setGate(undefined);
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
    knownRelease?: string;
  }): Promise<PointerRecordResult> {
    return stub(this.env, args.slug).readPointerRecord(args.app, args.knownRelease);
  }

  async readLabelRecord(args: {
    slug: string;
    label: string;
    knownRelease?: string;
  }): Promise<PointerRecordResult> {
    return stub(this.env, args.slug).readLabelRecord(args.label, args.knownRelease);
  }

  async readServedRecord(args: {
    slug: string;
    storageCoordinate: string;
  }): Promise<ServedRecordResult> {
    return stub(this.env, args.slug).readServedRecord(args.storageCoordinate);
  }

  async listOtherAppReleases(args: { slug: string; storageCoordinate: string }): Promise<string[]> {
    return stub(this.env, args.slug).listOtherAppReleases(args.storageCoordinate);
  }

  async readGate(args: { slug: string }): Promise<string | undefined> {
    return stub(this.env, args.slug).readGate();
  }
}
