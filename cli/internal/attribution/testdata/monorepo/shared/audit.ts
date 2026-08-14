import { postgres } from "../sdk/index.js";

export const auditDb = postgres("audit-db");
