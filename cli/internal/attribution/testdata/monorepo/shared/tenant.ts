import { postgres } from "../sdk/index.js";

const suffix = ["ten", "ant"].join("");
export const tenantDb = postgres(suffix + "-db");
