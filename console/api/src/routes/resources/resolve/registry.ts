import type { resourceAssignment } from "@console/db/schema";
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

export interface ResourceTypeHandler {
  link(name: string, assignment: ResourceAssignmentRow): string;
  provision(identity: ResourceIdentity): Promise<ProvisionedResource>;
}

const postgresHandler: ResourceTypeHandler = {
  link(name, assignment) {
    const admin = getCloudAdminUrl();
    return JSON.stringify({
      name,
      postgres: {
        host: admin.hostname,
        port: Number(admin.port || 5432),
        database: assignment.databaseName,
        username: assignment.roleName,
        password: assignment.password,
      },
    });
  },

  async provision(identity) {
    const { roleName, databaseName } = deriveResourceIdentifiers(identity);
    const password = generatePassword();

    await withCloudAdminClient(async (client) => {
      await client.query(
        `CREATE ROLE ${quoteIdentifier(roleName)} WITH LOGIN PASSWORD '${password}'`,
      );
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
