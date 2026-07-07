import { createHash, randomBytes } from "node:crypto";

// Postgres silently truncates identifiers longer than 63 bytes
// (NAMEDATALEN - 1) instead of erroring, so a hash derived from
// user-controlled identity must be capped well under that itself to stay
// collision-safe - relying on Postgres's own truncation would reintroduce
// exactly the collisions hashing is meant to avoid.
const MAX_IDENTIFIER_LENGTH = 63;

export interface ResourceIdentity {
  userId: string;
  projectId: string;
  resourceName: string;
  resourceType: string;
}

function deriveIdentifier(prefix: string, identity: ResourceIdentity): string {
  // NUL-joined so e.g. userId "a"/resourceName "bc" can't collide with
  // userId "ab"/resourceName "c".
  const key = [
    identity.userId,
    identity.projectId,
    identity.resourceName,
    identity.resourceType,
  ].join("\u0000");
  const digest = createHash("sha256").update(key).digest("hex").slice(0, 32);
  return `${prefix}${digest}`.slice(0, MAX_IDENTIFIER_LENGTH);
}

// Deterministic per-(user, project, resource, type) role/database names:
// same identity always yields the same identifiers (a retry after a crash
// reuses rather than leaks an orphaned role/db), collisions are as unlikely
// as a SHA-256 collision, and the fixed hex-digest alphabet is inherently
// SQL-safe regardless of what the identity strings themselves contain.
export function deriveResourceIdentifiers(identity: ResourceIdentity): {
  roleName: string;
  databaseName: string;
} {
  return {
    roleName: deriveIdentifier("ocel_role_", identity),
    databaseName: deriveIdentifier("ocel_db_", identity),
  };
}

// Double-quotes a Postgres identifier for safe interpolation into DDL that
// doesn't support bind parameters (CREATE ROLE/DATABASE), escaping embedded
// double quotes per the SQL standard - defense in depth, since the
// generated identifiers above never contain one.
export function quoteIdentifier(identifier: string): string {
  return `"${identifier.replace(/"/g, '""')}"`;
}

// base64url is inherently free of quote characters, so the result is safe
// to embed directly in a SQL string literal.
export function generatePassword(): string {
  return randomBytes(24).toString("base64url");
}
