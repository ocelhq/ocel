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

export interface ResourceTypeHandler {
  buildConnectionString(assignment: ResourceAssignmentRow): string;
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
