// Local-dev-only Bun harness: mounts @repo/api's handlers natively (no
// adapter, the exact functions Next's route files re-export) - including the
// real POST /api/resources/resolve handler, at the same path prod serves it
// - alongside one dev-only project-config passthrough, so `ocel dev` can
// fast-start against a real running server instead of deadlocking on
// `next dev` (see ocelhq-z7j). Not used in production - apps/web still runs
// on Next there.
import {
  authHandler,
  createProject,
  getProjectById,
  listProjects,
  resolveResources,
} from "@repo/api";
import { handleDevProjectConfig } from "./dev-handlers";

export async function handleRequest(request: Request): Promise<Response> {
  const url = new URL(request.url);
  const { pathname } = url;

  if (pathname === "/health") {
    return new Response("ok", { status: 200 });
  }

  if (pathname.startsWith("/api/auth/")) {
    return authHandler(request);
  }

  if (pathname === "/api/projects") {
    if (request.method === "GET") {
      return listProjects(request);
    }
    if (request.method === "POST") {
      return createProject(request);
    }
    return new Response("Method Not Allowed", { status: 405 });
  }

  const projectByIdMatch = pathname.match(/^\/api\/projects\/([^/]+)$/);
  if (projectByIdMatch) {
    if (request.method === "GET") {
      return getProjectById(request, decodeURIComponent(projectByIdMatch[1]));
    }
    return new Response("Method Not Allowed", { status: 405 });
  }

  if (pathname === "/api/resources/resolve" && request.method === "POST") {
    return resolveResources(request);
  }

  if (pathname === "/dev/project-config" && request.method === "POST") {
    return handleDevProjectConfig(request);
  }

  return new Response("Not found", { status: 404 });
}

// PORT is how internal/localharness.Spawn hands this process its listening
// port on the Go CLI side.
if (import.meta.main) {
  const port = Number(process.env.PORT ?? 3001);
  Bun.serve({ port, fetch: handleRequest });
  console.log(`local-api-server listening on http://localhost:${port}`);
}
