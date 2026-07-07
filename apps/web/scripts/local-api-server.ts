// Local-dev-only Bun harness: mounts @repo/api's handlers natively (no
// adapter, the exact functions Next's route files re-export) alongside two
// dev-only provisioning-handshake endpoints, so `ocel dev` can fast-start
// against a real running server instead of deadlocking on `next dev` (see
// ocelhq-z7j). Not used in production - apps/web still runs on Next there.
import {
  authHandler,
  createProject,
  getProjectById,
  listProjects,
} from "@repo/api";
import { handleDevProjectConfig, handleDevProvision } from "./dev-handlers";

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

  if (pathname === "/dev/project-config" && request.method === "POST") {
    return handleDevProjectConfig(request);
  }

  if (pathname === "/dev/provision" && request.method === "POST") {
    return handleDevProvision(request);
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
