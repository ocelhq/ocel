import type { resourceAssignment } from "@repo/db/schema";
import { getCloudAdminUrl, withCloudAdminClient } from "./cloud-admin";
import {
  deriveResourceIdentifiers,
  generatePassword,
  quoteIdentifier,
  type ResourceIdentity,
} from "./identifiers";

type ResourceAssignmentRow = typeof resourceAssignment.$inferSelect;

export interface ProvisionedResource {
  databaseName: string;
  roleName: string;
  password: string;
}

// One entry per resource type declared via the SDK (e.g. `postgres("main")`
// -> type "POSTGRES"), keyed by the same canonical uppercase name the CLI
// already uses on the wire (see internal/provision.ResourceTypeName and
// packages/ocel/src/utils/get-config.ts).
export interface ResourceTypeHandler {
  // Builds a ready-to-use connection string from an assignment row that
  // already exists.
  buildConnectionString(assignment: ResourceAssignmentRow): string;
  // First-request provisioning: connects to the cloud cluster as admin and
  // creates a per-user role + database, returning the fields the caller
  // persists as the new assignment row.
  provision(identity: ResourceIdentity): Promise<ProvisionedResource>;
}

const postgresHandler: ResourceTypeHandler = {
  buildConnectionString(assignment) {
    const url = getCloudAdminUrl();
    url.username = assignment.roleName;
    url.password = assignment.password;
    url.pathname = `/${assignment.databaseName}`;
    return url.toString();
  },

  async provision(identity) {
    const { roleName, databaseName } = deriveResourceIdentifiers(identity);
    const password = generatePassword();

    await withCloudAdminClient(async (client) => {
      await client.query(
        `CREATE ROLE ${quoteIdentifier(roleName)} WITH LOGIN PASSWORD '${password}'`,
      );
      // Can't run inside the same implicit transaction as CREATE ROLE -
      // CREATE DATABASE requires its own statement anyway, so this is just
      // a second query on the same connection.
      await client.query(
        `CREATE DATABASE ${quoteIdentifier(databaseName)} OWNER ${quoteIdentifier(roleName)}`,
      );
    });

    return { databaseName, roleName, password };
  },
};

export const resourceTypeRegistry: Record<string, ResourceTypeHandler> = {
  POSTGRES: postgresHandler,
};
