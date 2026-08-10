import { createHash, randomBytes } from "node:crypto";

const MAX_IDENTIFIER_LENGTH = 63;

export interface ResourceIdentity {
  userId: string;
  projectId: string;
  resourceName: string;
  resourceType: string;
}

function deriveIdentifier(prefix: string, identity: ResourceIdentity): string {
  const key = [
    identity.userId,
    identity.projectId,
    identity.resourceName,
    identity.resourceType,
  ].join("\u0000");
  const digest = createHash("sha256").update(key).digest("hex").slice(0, 32);
  return `${prefix}${digest}`.slice(0, MAX_IDENTIFIER_LENGTH);
}

export function deriveResourceIdentifiers(identity: ResourceIdentity): {
  roleName: string;
  databaseName: string;
} {
  return {
    roleName: deriveIdentifier("ocel_role_", identity),
    databaseName: deriveIdentifier("ocel_db_", identity),
  };
}

export function quoteIdentifier(identifier: string): string {
  return `"${identifier.replace(/"/g, '""')}"`;
}

export function generatePassword(): string {
  return randomBytes(24).toString("base64url");
}
