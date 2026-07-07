import type { resourceAssignment } from "@repo/db/schema";

type ResourceAssignmentRow = typeof resourceAssignment.$inferSelect;

// One entry per resource type declared via the SDK (e.g. `postgres("main")`
// -> type "POSTGRES"), keyed by the same canonical uppercase name the CLI
// already uses on the wire (see internal/provision.ResourceTypeName and
// packages/ocel/src/utils/get-config.ts).
export interface ResourceTypeHandler {
  // Builds a ready-to-use connection string from an assignment row that
  // already exists. Creating a new assignment (first-request provisioning)
  // lands in ocelhq-amu.3 - this slice only reuses existing rows.
  buildConnectionString(assignment: ResourceAssignmentRow): string;
}

// The local "cloud" cluster's admin connection string (a docker-compose
// Postgres standing in for Aurora Serverless v2 in prod - see the epic's
// design decisions). Resolve never runs SQL against it in this slice; it
// only borrows its host/port to build a connection string for an existing
// per-user role+database.
function requireCloudAdminUrl(): URL {
  const raw = process.env.OCEL_CLOUD_ADMIN_URL;
  if (!raw) {
    throw new Error(
      "OCEL_CLOUD_ADMIN_URL is not set - cannot build a resource connection string",
    );
  }
  return new URL(raw);
}

const postgresHandler: ResourceTypeHandler = {
  buildConnectionString(assignment) {
    const url = requireCloudAdminUrl();
    url.username = assignment.roleName;
    url.password = assignment.password;
    url.pathname = `/${assignment.databaseName}`;
    return url.toString();
  },
};

export const resourceTypeRegistry: Record<string, ResourceTypeHandler> = {
  POSTGRES: postgresHandler,
};
