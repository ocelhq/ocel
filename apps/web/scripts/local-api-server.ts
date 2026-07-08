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
import { Hono } from "hono";
import { handleDevProjectConfig } from "./dev-handlers";

const app = new Hono();

app.get("/health", (c) => c.text("ok", 200));

app.on(["GET", "POST", "PUT", "DELETE", "PATCH"], "/api/auth/*", (c) =>
  authHandler(c.req.raw),
);

app.get("/api/projects", (c) => listProjects(c.req.raw));
app.post("/api/projects", (c) => createProject(c.req.raw));

app.get("/api/projects/:id", (c) =>
  getProjectById(c.req.raw, c.req.param("id")),
);

app.post("/api/resources/resolve", (c) => resolveResources(c.req.raw));

app.post("/dev/project-config", (c) => handleDevProjectConfig(c.req.raw));

export const handleRequest = app.fetch;

// PORT is how internal/localharness.Spawn hands this process its listening
// port on the Go CLI side.
if (import.meta.main) {
  const port = Number(process.env.PORT ?? 3001);
  Bun.serve({ port, fetch: handleRequest });
  console.log(`local-api-server listening on http://localhost:${port}`);
}
