import { WorkerEntrypoint } from "cloudflare:workers";

import { authorized } from "./auth";
import { DeploymentsStore } from "./deployments-do";
import type { DeploymentRecord, Promotion } from "./store";
import type { Env } from "./env";

export { DeploymentsStore };

// One DO instance per project (this worker is deployed once per project), so
// every stub resolves the same fixed name.
function stub(env: Env) {
  return env.DEPLOYMENTS_DO.get(env.DEPLOYMENTS_DO.idFromName("root"));
}

async function readJson<T>(request: Request): Promise<T | undefined> {
  try {
    return (await request.json()) as T;
  } catch {
    return undefined;
  }
}

// The deployments store's two access paths (ADR 0002):
//
// - fetch() is the authenticated write endpoint the deploy host calls over
//   plain HTTP (it runs outside Cloudflare's network, so it can't use RPC):
//   putStaged / promote / history / prune / the version stamp. Every route
//   requires the project write-secret.
// - activeBuildId / record are RPC methods the frozen generic worker calls
//   through its service binding to this worker. They carry no secret — the
//   trust boundary is the binding itself, which is only ever reachable from
//   another Worker in the same account, never over the public internet.
export default class extends WorkerEntrypoint<Env> {
  async fetch(request: Request): Promise<Response> {
    if (!(await authorized(request, this.env.WRITE_SECRET))) {
      return new Response("Unauthorized", { status: 401 });
    }

    const url = new URL(request.url);
    const store = stub(this.env);

    if (request.method === "PUT" && url.pathname === "/staged") {
      const record = await readJson<DeploymentRecord>(request);
      if (!record) return new Response("Bad Request", { status: 400 });
      await store.putStaged(record);
      return new Response(null, { status: 204 });
    }

    if (request.method === "POST" && url.pathname === "/promote") {
      const body = await readJson<Promotion>(request);
      if (!body?.promotionId || !body.builds) {
        return new Response("Bad Request", { status: 400 });
      }
      const { conflict } = await store.promote(body);
      if (conflict) return new Response(conflict, { status: 409 });
      return new Response(null, { status: 204 });
    }

    if (request.method === "GET" && url.pathname === "/history") {
      return Response.json(await store.history());
    }

    if (request.method === "POST" && url.pathname === "/prune") {
      const body = await readJson<{ keepN: number }>(request);
      if (typeof body?.keepN !== "number") {
        return new Response("Bad Request", { status: 400 });
      }
      return Response.json(await store.prune(body.keepN));
    }

    if (request.method === "GET" && url.pathname === "/version-stamp") {
      return Response.json({ version: (await store.versionStamp()) ?? null });
    }

    if (request.method === "PUT" && url.pathname === "/version-stamp") {
      const body = await readJson<{ version: string }>(request);
      if (!body?.version) return new Response("Bad Request", { status: 400 });
      await store.setVersionStamp(body.version);
      return new Response(null, { status: 204 });
    }

    return new Response("Not Found", { status: 404 });
  }

  async activeBuildId(app: string): Promise<string | undefined> {
    return stub(this.env).activeBuildId(app);
  }

  async record(app: string, buildId: string): Promise<DeploymentRecord | undefined> {
    return stub(this.env).record(app, buildId);
  }
}
