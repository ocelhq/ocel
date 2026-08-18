import { postgres } from "../sdk/index.js";

export const metricsDb = postgres("metrics-db");
