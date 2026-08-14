import { db } from "./db-alias.js";
import { uploads } from "../../../shared/files.js";
import { tenantDb } from "../../../shared/tenant.js";

export function handler() {
  return { db, uploads, tenantDb };
}
